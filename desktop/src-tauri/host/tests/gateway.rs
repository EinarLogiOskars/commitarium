//! The gateway over real TLS, against small stand-ins for what it forwards to.
#![cfg(unix)]

use commitarium_host::bridge::Bridge;
use commitarium_host::device_keys::{host_client, pair, Identity};
use commitarium_host::gateway::{Gateway, Upstreams};
use std::io::Read;
use std::sync::Arc;
use std::time::Duration;
use tokio::net::TcpListener;

/// Echoes each request's path and headers. `/stream` stays open for a device
/// to be cut off from.
fn upstream() -> u16 {
    let server = tiny_http::Server::http("127.0.0.1:0").unwrap();
    let port = server.server_addr().to_ip().unwrap().port();
    std::thread::spawn(move || {
        for request in server.incoming_requests() {
            std::thread::spawn(move || {
                if request.url() == "/stream" {
                    let (mut reader, writer) = std::io::pipe().unwrap();
                    use std::io::Write;
                    let mut writer = writer;
                    writer.write_all(b"first\n").unwrap();
                    std::thread::spawn(move || {
                        std::thread::sleep(Duration::from_secs(6));
                        drop(writer);
                    });
                    let mut chunk = [0u8; 6];
                    reader.read_exact(&mut chunk).unwrap();
                    let body = std::io::Cursor::new(chunk.to_vec()).chain(reader);
                    let _ = request.respond(tiny_http::Response::new(
                        200.into(),
                        vec![],
                        body,
                        None,
                        None,
                    ));
                    return;
                }
                let headers: Vec<String> = request
                    .headers()
                    .iter()
                    .map(|h| format!("{}={}", h.field.as_str().as_str().to_lowercase(), h.value))
                    .collect();
                let body = format!("{} {}", request.url(), headers.join(";"));
                let _ = request.respond(tiny_http::Response::from_string(body));
            });
        }
    });
    port
}

struct Fixture {
    gateway: Arc<Gateway>,
    upstream: u16,
    address: String,
    port: u16,
    _dir: tempfile::TempDir,
}

async fn start() -> Fixture {
    let dir = tempfile::tempdir().unwrap();
    let port = upstream();
    let gateway = Gateway::open(
        dir.path(),
        Upstreams {
            host_port: port,
            host_token: "host-token".into(),
            coordinator_port: port,
            forgejo_port: port,
            is_preview: Box::new(move |asked| asked == port),
        },
    )
    .unwrap();
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let gateway_port = listener.local_addr().unwrap().port();
    tokio::spawn(gateway.clone().serve(listener));
    Fixture {
        gateway,
        upstream: port,
        address: format!("127.0.0.1:{gateway_port}"),
        port: gateway_port,
        _dir: dir,
    }
}

impl Fixture {
    fn line(&self) -> String {
        let opened = self.gateway.open_pairing(self.port).unwrap();
        format!("commitarium://{}#{}", self.address, opened.code)
    }

    async fn paired(&self, name: &str) -> (Identity, reqwest::Client) {
        let device = Identity::generate().unwrap();
        let host = pair(&self.line(), name, &device).await.unwrap();
        let client = host_client(&device, &host.fingerprint).unwrap();
        (device, client)
    }

    fn url(&self, path: &str) -> String {
        format!("https://{}{path}", self.address)
    }
}

#[tokio::test(flavor = "multi_thread")]
async fn a_stranger_gets_nothing_while_pairing_is_closed() {
    let fixture = start().await;
    let device = Identity::generate().unwrap();
    let fingerprint = fixture.gateway.fingerprint().unwrap();
    let client = host_client(&device, &fingerprint).unwrap();
    assert!(client
        .get(fixture.url("/coordinator/health"))
        .send()
        .await
        .is_err());
    assert!(client
        .post(fixture.url("/pair/start"))
        .body("{}")
        .send()
        .await
        .is_err());
}

#[tokio::test(flavor = "multi_thread")]
async fn a_stranger_during_pairing_reaches_only_pairing() {
    let fixture = start().await;
    fixture.line();
    let device = Identity::generate().unwrap();
    let fingerprint = fixture.gateway.fingerprint().unwrap();
    let client = host_client(&device, &fingerprint).unwrap();
    for path in ["/coordinator/health", "/host/v1/version", "/forgejo/"] {
        let answer = client.get(fixture.url(path)).send().await.unwrap();
        assert_eq!(answer.status(), 404, "{path}");
    }
}

#[tokio::test(flavor = "multi_thread")]
async fn a_paired_device_reaches_all_three_and_holds_no_token() {
    let fixture = start().await;
    let (_, client) = fixture.paired("mac").await;
    assert_eq!(fixture.gateway.devices()[0].name, "mac");
    assert_eq!(fixture.gateway.devices()[0].role, "owner");

    let host = client
        .get(fixture.url("/host/v1/version?x=1"))
        .header("Authorization", "Bearer from-device")
        .header("Origin", "https://evil.example")
        .send()
        .await
        .unwrap()
        .text()
        .await
        .unwrap();
    assert!(host.starts_with("/v1/version?x=1 "), "{host}");
    assert!(host.contains("authorization=Bearer host-token"), "{host}");
    assert!(!host.contains("from-device"), "{host}");
    assert!(!host.contains("origin="), "{host}");

    let coordinator = client
        .get(fixture.url("/coordinator/health"))
        .send()
        .await
        .unwrap()
        .text()
        .await
        .unwrap();
    assert!(coordinator.starts_with("/health "), "{coordinator}");
    assert!(!coordinator.contains("host-token"), "{coordinator}");

    let unknown = client.get(fixture.url("/hostile")).send().await.unwrap();
    assert_eq!(unknown.status(), 404);
}

#[tokio::test(flavor = "multi_thread")]
async fn a_code_works_once() {
    let fixture = start().await;
    let line = fixture.line();
    pair(&line, "first", &Identity::generate().unwrap())
        .await
        .unwrap();
    assert!(pair(&line, "second", &Identity::generate().unwrap())
        .await
        .is_err());
    assert_eq!(fixture.gateway.devices().len(), 1);
}

#[tokio::test(flavor = "multi_thread")]
async fn wrong_codes_end_pairing() {
    let fixture = start().await;
    let line = fixture.line();
    let wrong = format!("commitarium://{}#WRONGCOD", fixture.address);
    for _ in 0..3 {
        assert!(pair(&wrong, "guess", &Identity::generate().unwrap())
            .await
            .is_err());
    }
    assert!(pair(&line, "late", &Identity::generate().unwrap())
        .await
        .is_err());
    assert!(fixture.gateway.devices().is_empty());
}

#[tokio::test(flavor = "multi_thread")]
async fn a_code_expires() {
    let fixture = start().await;
    let opened = fixture
        .gateway
        .open_pairing_for(fixture.port, Duration::from_millis(50))
        .unwrap();
    tokio::time::sleep(Duration::from_millis(100)).await;
    let line = format!("commitarium://{}#{}", fixture.address, opened.code);
    assert!(pair(&line, "late", &Identity::generate().unwrap())
        .await
        .is_err());
}

#[tokio::test(flavor = "multi_thread")]
async fn a_revoked_device_is_refused_and_its_stream_ends() {
    let fixture = start().await;
    let (device, client) = fixture.paired("mac").await;
    let mut stream = client
        .get(fixture.url("/coordinator/stream"))
        .send()
        .await
        .unwrap();
    assert_eq!(&stream.chunk().await.unwrap().unwrap()[..], b"first\n");

    fixture
        .gateway
        .revoke(&device.fingerprint().unwrap())
        .unwrap();
    let ended = tokio::time::timeout(Duration::from_secs(3), stream.chunk()).await;
    assert!(matches!(ended, Ok(Ok(None)) | Ok(Err(_))), "{ended:?}");
    assert!(client
        .get(fixture.url("/coordinator/health"))
        .send()
        .await
        .is_err());
}

#[tokio::test(flavor = "multi_thread")]
async fn a_fake_host_is_refused() {
    let fixture = start().await;
    let (device, _) = fixture.paired("mac").await;
    let pinned_elsewhere = Identity::generate().unwrap().fingerprint().unwrap();
    let client = host_client(&device, &pinned_elsewhere).unwrap();
    assert!(client
        .get(fixture.url("/coordinator/health"))
        .send()
        .await
        .is_err());
}

#[tokio::test(flavor = "multi_thread")]
async fn devices_and_the_host_identity_outlast_a_restart() {
    let dir = tempfile::tempdir().unwrap();
    let upstreams = || Upstreams {
        host_port: 1,
        host_token: "t".into(),
        coordinator_port: 1,
        forgejo_port: 1,
        is_preview: Box::new(|_| false),
    };
    let first = Gateway::open(dir.path(), upstreams()).unwrap();
    let second = Gateway::open(dir.path(), upstreams()).unwrap();
    assert_eq!(first.fingerprint().unwrap(), second.fingerprint().unwrap());
}

async fn bridged(fixture: &Fixture) -> (Bridge, reqwest::Client) {
    let (_, client) = fixture.paired("mac").await;
    let bridge = Bridge::start(
        client,
        &fixture.address,
        "bridge-token".into(),
        "window-token".into(),
        vec![],
    )
    .await
    .unwrap();
    let local = reqwest::Client::builder().no_proxy().build().unwrap();
    (bridge, local)
}

#[tokio::test(flavor = "multi_thread")]
async fn the_bridge_carries_requests_and_keeps_its_token_local() {
    let fixture = start().await;
    let (bridge, local) = bridged(&fixture).await;
    let answer = local
        .get(format!("{}/v1/version", bridge.host_url()))
        .bearer_auth("bridge-token")
        .send()
        .await
        .unwrap();
    assert_eq!(answer.status(), 200);
    let text = answer.text().await.unwrap();
    assert!(text.starts_with("/v1/version "), "{text}");
    assert!(text.contains("authorization=Bearer host-token"), "{text}");
    assert!(!text.contains("bridge-token"), "{text}");

    let coordinator = local
        .post(format!("{}/health?a=1", bridge.coordinator_url()))
        .bearer_auth("bridge-token")
        .body("hello")
        .send()
        .await
        .unwrap()
        .text()
        .await
        .unwrap();
    assert!(coordinator.starts_with("/health?a=1 "), "{coordinator}");
}

#[tokio::test(flavor = "multi_thread")]
async fn the_bridge_answers_only_the_app() {
    let fixture = start().await;
    let (bridge, local) = bridged(&fixture).await;
    let url = format!("{}/v1/version", bridge.host_url());
    let status = |request: reqwest::RequestBuilder| async {
        request.send().await.unwrap().status().as_u16()
    };
    assert_eq!(status(local.get(&url)).await, 401);
    assert_eq!(
        status(local.get(&url).bearer_auth("wrong-token!")).await,
        401
    );
    assert_eq!(
        status(
            local
                .get(&url)
                .bearer_auth("bridge-token")
                .header("Origin", "https://evil.example")
        )
        .await,
        401
    );
}

#[tokio::test(flavor = "multi_thread")]
async fn an_app_window_reaches_the_coordinator_and_never_the_host() {
    let fixture = start().await;
    let (bridge, local) = bridged(&fixture).await;
    let status = |url: String, origin: &'static str| {
        let request = local
            .get(url)
            .bearer_auth("window-token")
            .header("Origin", origin);
        async move { request.send().await.unwrap().status().as_u16() }
    };
    let coordinator = format!("{}/health", bridge.coordinator_url());
    let host = format!("{}/v1/version", bridge.host_url());
    let page = "http://127.0.0.1:5555";
    assert_eq!(status(coordinator.clone(), page).await, 401);
    bridge.allow_origin(page.to_string());
    assert_eq!(status(coordinator, page).await, 200);
    assert_eq!(status(host, page).await, 401);
}

#[tokio::test(flavor = "multi_thread")]
async fn a_preview_reaches_the_app_through_a_tunnel() {
    let fixture = start().await;
    let (bridge, local) = bridged(&fixture).await;
    let mut status = serde_json::json!({
        "urls": [{ "url": format!("http://127.0.0.1:{}", fixture.upstream) }]
    });
    bridge.localize_previews(&mut status).await;
    let url = status["urls"][0]["url"].as_str().unwrap().to_string();
    assert_ne!(url, format!("http://127.0.0.1:{}", fixture.upstream));
    let page = local
        .get(format!("{url}/page"))
        .send()
        .await
        .unwrap()
        .text()
        .await
        .unwrap();
    assert!(page.starts_with("/page "), "{page}");
}

#[tokio::test(flavor = "multi_thread")]
async fn a_port_that_is_no_preview_stays_closed() {
    let fixture = start().await;
    let (_, client) = fixture.paired("mac").await;
    let answer = client
        .post(fixture.url("/tunnel/1"))
        .header("Connection", "upgrade")
        .header("Upgrade", "commitarium-tunnel")
        .send()
        .await
        .unwrap();
    assert_eq!(answer.status(), 404);
}
