//! The app's side of remote mode. A loopback server the webview talks to as it
//! talks to a local host: this holds the device's key, which the webview never
//! sees, and carries each request to the gateway on the server. So the UI only
//! learns a different address, and its HTTP permission stays on loopback.
//!
//! Like the host's loopback API, it is open to every local process, and it
//! holds what a device can do, so it has the same one check.

use futures_util::{StreamExt, TryStreamExt};
use http_body_util::{combinators::BoxBody, BodyExt, BodyStream, Empty, StreamBody};
use hyper::body::{Bytes, Frame, Incoming};
use hyper::header::{
    HeaderMap, AUTHORIZATION, CONNECTION, HOST, ORIGIN, TRANSFER_ENCODING, UPGRADE,
};
use hyper::service::service_fn;
use hyper::{Request, Response, StatusCode};
use hyper_util::rt::TokioIo;
use serde_json::Value;
use std::collections::HashMap;
use std::sync::{Arc, Mutex};
use tokio::io::copy_bidirectional;
use tokio::net::TcpListener;

use crate::device_keys::{TUNNEL_PREFIX, TUNNEL_PROTOCOL};

const LOCAL_PREVIEW: &str = "http://127.0.0.1:";

struct Shared {
    client: reqwest::Client,
    server: String,
    token: String,
    host: String,
    origins: Vec<String>,
    /// A preview's port on the server, and the local port standing for it.
    tunnels: Mutex<HashMap<u16, u16>>,
}

#[derive(Clone)]
pub struct Bridge {
    shared: Arc<Shared>,
    port: u16,
}

impl Bridge {
    /// Listens on a new loopback port. `client` comes from `host_client`, and
    /// `address` is the server's, with its port.
    pub async fn start(
        client: reqwest::Client,
        address: &str,
        token: String,
        origins: Vec<String>,
    ) -> Result<Self, String> {
        let listener = TcpListener::bind("127.0.0.1:0")
            .await
            .map_err(|e| format!("listen: {e}"))?;
        let port = listener
            .local_addr()
            .map_err(|e| format!("listen: {e}"))?
            .port();
        let shared = Arc::new(Shared {
            client,
            server: format!("https://{address}"),
            token,
            host: format!("127.0.0.1:{port}"),
            origins,
            tunnels: Mutex::default(),
        });
        tokio::spawn(accept(listener, shared.clone()));
        Ok(Self { shared, port })
    }

    pub fn base_url(&self) -> String {
        format!("http://127.0.0.1:{}", self.port)
    }

    pub fn host_url(&self) -> String {
        format!("http://127.0.0.1:{}/host", self.port)
    }

    pub fn coordinator_url(&self) -> String {
        format!("http://127.0.0.1:{}/coordinator", self.port)
    }

    /// Points each preview address in a status at a local port that reaches it.
    pub async fn localize_previews(&self, status: &mut Value) {
        let Some(urls) = status.get_mut("urls").and_then(Value::as_array_mut) else {
            return;
        };
        for open in urls {
            let port = open["url"]
                .as_str()
                .and_then(|url| url.strip_prefix(LOCAL_PREVIEW))
                .and_then(|port| port.parse().ok());
            if let Some(port) = port {
                if let Ok(local) = self.tunnel(port).await {
                    open["url"] = Value::String(format!("{LOCAL_PREVIEW}{local}"));
                }
            }
        }
    }

    async fn tunnel(&self, remote: u16) -> Result<u16, String> {
        if let Some(local) = self.tunnels().get(&remote) {
            return Ok(*local);
        }
        let listener = TcpListener::bind("127.0.0.1:0")
            .await
            .map_err(|e| format!("listen: {e}"))?;
        let local = listener
            .local_addr()
            .map_err(|e| format!("listen: {e}"))?
            .port();
        self.tunnels().insert(remote, local);
        tokio::spawn(carry_tunnel(listener, self.shared.clone(), remote));
        Ok(local)
    }

    fn tunnels(&self) -> std::sync::MutexGuard<'_, HashMap<u16, u16>> {
        self.shared.tunnels.lock().expect("the tunnels stay usable")
    }
}

async fn carry_tunnel(listener: TcpListener, shared: Arc<Shared>, remote: u16) {
    while let Ok((mut local, _)) = listener.accept().await {
        let shared = shared.clone();
        tokio::spawn(async move {
            let answer = shared
                .client
                .post(format!("{}{TUNNEL_PREFIX}{remote}", shared.server))
                .header(CONNECTION, "upgrade")
                .header(UPGRADE, TUNNEL_PROTOCOL)
                .send()
                .await;
            let Ok(answer) = answer else { return };
            if answer.status() != StatusCode::SWITCHING_PROTOCOLS {
                return;
            }
            if let Ok(mut upgraded) = answer.upgrade().await {
                let _ = copy_bidirectional(&mut local, &mut upgraded).await;
            }
        });
    }
}

async fn accept(listener: TcpListener, shared: Arc<Shared>) {
    while let Ok((stream, _)) = listener.accept().await {
        let shared = shared.clone();
        tokio::spawn(async move {
            let service = service_fn(move |request| {
                let shared = shared.clone();
                async move { Ok::<_, std::convert::Infallible>(answer(&shared, request).await) }
            });
            let _ = hyper::server::conn::http1::Builder::new()
                .serve_connection(TokioIo::new(stream), service)
                .await;
        });
    }
}

type Reply = Response<BoxBody<Bytes, reqwest::Error>>;

fn plain(status: StatusCode) -> Reply {
    let mut response = Response::new(Empty::new().map_err(|never| match never {}).boxed());
    *response.status_mut() = status;
    response
}

fn allowed(shared: &Shared, headers: &HeaderMap) -> bool {
    let text = |name| headers.get(name).and_then(|value| value.to_str().ok());
    let known_origin = text(ORIGIN).is_none_or(|origin| shared.origins.iter().any(|o| o == origin));
    let token = text(AUTHORIZATION)
        .and_then(|value| value.strip_prefix("Bearer "))
        .unwrap_or("");
    text(HOST) == Some(shared.host.as_str())
        && known_origin
        && token.len() == shared.token.len()
        && token
            .bytes()
            .zip(shared.token.bytes())
            .fold(0, |diff, (a, b)| diff | (a ^ b))
            == 0
}

async fn answer(shared: &Shared, request: Request<Incoming>) -> Reply {
    if !allowed(shared, request.headers()) {
        return plain(StatusCode::UNAUTHORIZED);
    }
    let (parts, body) = request.into_parts();
    let path = parts.uri.path();
    if !(path.starts_with("/host/") || path.starts_with("/coordinator/")) {
        return plain(StatusCode::NOT_FOUND);
    }
    let query = parts
        .uri
        .query()
        .map(|query| format!("?{query}"))
        .unwrap_or_default();
    let mut headers = parts.headers;
    for name in [AUTHORIZATION, ORIGIN, HOST, CONNECTION, TRANSFER_ENCODING] {
        headers.remove(name);
    }
    let body = BodyStream::new(body).filter_map(|frame| async {
        match frame {
            Ok(frame) => frame.into_data().ok().map(Ok),
            Err(error) => Some(Err(error)),
        }
    });
    let sent = shared
        .client
        .request(parts.method, format!("{}{path}{query}", shared.server))
        .headers(headers)
        .body(reqwest::Body::wrap_stream(body))
        .send()
        .await;
    let Ok(response) = sent else {
        return plain(StatusCode::BAD_GATEWAY);
    };
    let status = response.status();
    let mut headers = response.headers().clone();
    for name in [CONNECTION, TRANSFER_ENCODING] {
        headers.remove(name);
    }
    let mut reply = Response::new(BodyExt::boxed(StreamBody::new(
        response.bytes_stream().map_ok(Frame::data),
    )));
    *reply.status_mut() = status;
    *reply.headers_mut() = headers;
    reply
}
