//! The host's only door to a network (ADR-040, threat model): TLS 1.3 on one
//! port, with each paired device known by its certificate. A device reaches
//! the host's API, the coordinator and Forgejo on loopback through here, and
//! the gateway adds the host's token itself, so no device ever holds it.
//!
//! Without a paired device's certificate, a connection gets the pairing routes
//! while pairing is open, and otherwise nothing: it ends before a request is
//! read.

use base64::{engine::general_purpose::STANDARD as BASE64, Engine};
use http_body_util::{combinators::BoxBody, BodyExt, Full, Limited};
use hyper::body::{Bytes, Incoming};
use hyper::header::{AUTHORIZATION, HOST, ORIGIN};
use hyper::service::service_fn;
use hyper::{Method, Request, Response, StatusCode, Uri};
use hyper_util::client::legacy::{connect::HttpConnector, Client};
use hyper_util::rt::{TokioExecutor, TokioIo};
use rustls::crypto::verify_tls13_signature;
use rustls::pki_types::{CertificateDer, UnixTime};
use rustls::server::danger::{ClientCertVerified, ClientCertVerifier};
use rustls::{DigitallySignedStruct, DistinguishedName, ServerConfig, SignatureScheme};
use serde::{Deserialize, Serialize};
use std::fs::{self, OpenOptions};
use std::io::{ErrorKind, Write};
use std::os::unix::fs::OpenOptionsExt;
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};
use tokio::net::TcpListener;
use tokio::sync::watch;
use tokio_rustls::TlsAcceptor;

use crate::device_keys::{
    fingerprint, pake_answer, proof, proof_matches, provider, Identity, PairDone, PairFinish,
    PairStart, DEVICE_SIDE, HOST_SIDE,
};

pub const DEFAULT_PORT: u16 = 7443;
const IDENTITY_FILE: &str = "host-identity.json";
const DEVICES_FILE: &str = "devices.json";
const PAIRING_WINDOW: Duration = Duration::from_secs(5 * 60);
/// Each attempt is one guess at the code.
const PAIRING_ATTEMPTS: u8 = 3;
const HANDSHAKE_PATIENCE: Duration = Duration::from_secs(10);
const MAX_PAIRING_BODY: usize = 16 * 1024;
/// Crockford's alphabet: no letters that read as numbers.
const CODE_ALPHABET: &[u8; 32] = b"0123456789ABCDEFGHJKMNPQRSTVWXYZ";

/// A paired device. Every device is an owner for now.
#[derive(Clone, Debug, Deserialize, PartialEq, Serialize)]
pub struct Device {
    pub fingerprint: String,
    pub name: String,
    pub role: String,
    pub paired_at: u64,
}

/// The loopback services a device reaches through the gateway.
pub struct Upstreams {
    pub host_port: u16,
    pub host_token: String,
    pub coordinator_port: u16,
    pub forgejo_port: u16,
}

struct Window {
    code: String,
    closes: Instant,
    attempts: u8,
    /// The key of the attempt in progress.
    key: Option<Vec<u8>>,
}

pub struct Gateway {
    dir: PathBuf,
    identity: Identity,
    upstreams: Upstreams,
    devices: Mutex<Vec<Device>>,
    window: Mutex<Option<Window>>,
    /// Ticks when a device is revoked, so its connections look again.
    revoked: watch::Sender<u64>,
    client: Client<HttpConnector, Incoming>,
}

/// Where to pair: the code and how long it lasts.
#[derive(Debug, Deserialize, Serialize)]
pub struct PairingOpened {
    pub code: String,
    pub port: u16,
    pub seconds: u64,
}

impl Gateway {
    /// Loads the host's identity and paired devices from `dir`, making the
    /// identity on first use. It lives for good, because devices pin it.
    pub fn open(dir: &Path, upstreams: Upstreams) -> Result<Arc<Self>, String> {
        let identity = match read_json::<Identity>(&dir.join(IDENTITY_FILE))? {
            Some(identity) => identity,
            None => {
                let identity = Identity::generate()?;
                write_private(&dir.join(IDENTITY_FILE), &identity)?;
                identity
            }
        };
        let devices = read_json(&dir.join(DEVICES_FILE))?.unwrap_or_default();
        Ok(Arc::new(Self {
            dir: dir.to_path_buf(),
            identity,
            upstreams,
            devices: Mutex::new(devices),
            window: Mutex::new(None),
            revoked: watch::channel(0).0,
            client: Client::builder(TokioExecutor::new()).build_http(),
        }))
    }

    pub fn fingerprint(&self) -> Result<String, String> {
        self.identity.fingerprint()
    }

    pub fn devices(&self) -> Vec<Device> {
        self.devices_held().clone()
    }

    fn devices_held(&self) -> std::sync::MutexGuard<'_, Vec<Device>> {
        self.devices.lock().expect("the device list stays usable")
    }

    fn window_held(&self) -> std::sync::MutexGuard<'_, Option<Window>> {
        self.window.lock().expect("the pairing window stays usable")
    }

    fn knows(&self, device: &str) -> bool {
        self.devices_held().iter().any(|d| d.fingerprint == device)
    }

    fn pairing_is_open(&self) -> bool {
        let mut window = self.window_held();
        if window.as_ref().is_some_and(|w| Instant::now() >= w.closes) {
            *window = None;
        }
        window.is_some()
    }

    /// Opens pairing for one device, replacing any window already open.
    pub fn open_pairing(&self, port: u16) -> Result<PairingOpened, String> {
        self.open_pairing_for(port, PAIRING_WINDOW)
    }

    pub fn open_pairing_for(&self, port: u16, lasts: Duration) -> Result<PairingOpened, String> {
        let code = pairing_code()?;
        *self.window_held() = Some(Window {
            code: code.clone(),
            closes: Instant::now() + lasts,
            attempts: 0,
            key: None,
        });
        Ok(PairingOpened {
            code,
            port,
            seconds: lasts.as_secs(),
        })
    }

    /// Removes a device and ends its open connections.
    pub fn revoke(&self, device: &str) -> Result<(), String> {
        let mut devices = self.devices_held();
        let before = devices.len();
        devices.retain(|d| d.fingerprint != device && d.name != device);
        if devices.len() == before {
            return Err(format!("no device matches {device}"));
        }
        write_private(&self.dir.join(DEVICES_FILE), &*devices)?;
        drop(devices);
        self.revoked.send_modify(|tick| *tick += 1);
        Ok(())
    }

    fn start_pairing(&self, start: PairStart) -> Option<PairStart> {
        let message = BASE64.decode(start.message).ok()?;
        let mut held = self.window_held();
        let window = held.as_mut()?;
        window.attempts += 1;
        if window.attempts > PAIRING_ATTEMPTS {
            *held = None;
            return None;
        }
        let (answer, key) = pake_answer(&window.code, &message).ok()?;
        window.key = Some(key);
        Some(PairStart {
            message: BASE64.encode(answer),
        })
    }

    fn finish_pairing(&self, finish: PairFinish) -> Option<PairDone> {
        let mut held = self.window_held();
        let window = held.as_mut()?;
        let key = window.key.take()?;
        let host = self.fingerprint().ok()?;
        let device = fingerprint(&BASE64.decode(&finish.certificate).ok()?);
        let seen = BASE64.decode(&finish.proof).ok()?;
        if !proof_matches(&key, DEVICE_SIDE, &host, &device, &seen) {
            return None;
        }
        // The code is spent, whatever happens next.
        *held = None;
        drop(held);

        let mut devices = self.devices_held();
        devices.retain(|d| d.fingerprint != device);
        devices.push(Device {
            fingerprint: device.clone(),
            name: finish.name,
            role: "owner".to_string(),
            paired_at: SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .map_or(0, |since| since.as_secs()),
        });
        write_private(&self.dir.join(DEVICES_FILE), &*devices).ok()?;
        Some(PairDone {
            proof: BASE64.encode(proof(&key, HOST_SIDE, &host, &device)),
        })
    }

    fn tls(&self) -> Result<TlsAcceptor, String> {
        let config = ServerConfig::builder_with_provider(provider())
            .with_protocol_versions(&[&rustls::version::TLS13])
            .map_err(|e| e.to_string())?
            .with_client_cert_verifier(Arc::new(DeviceVerifier))
            .with_single_cert(
                vec![self.identity.certificate_der()?],
                self.identity.key_der()?,
            )
            .map_err(|e| e.to_string())?;
        Ok(TlsAcceptor::from(Arc::new(config)))
    }

    /// Serves `listener` until the task ends.
    pub async fn serve(self: Arc<Self>, listener: TcpListener) -> Result<(), String> {
        let tls = self.tls()?;
        loop {
            let Ok((stream, _)) = listener.accept().await else {
                continue;
            };
            let gateway = self.clone();
            let tls = tls.clone();
            tokio::spawn(async move {
                if let Ok(Ok(stream)) =
                    tokio::time::timeout(HANDSHAKE_PATIENCE, tls.accept(stream)).await
                {
                    let device = stream
                        .get_ref()
                        .1
                        .peer_certificates()
                        .and_then(<[_]>::first)
                        .map(|certificate| fingerprint(certificate));
                    gateway.serve_connection(stream, device).await;
                }
            });
        }
    }

    async fn serve_connection<S>(self: Arc<Self>, stream: S, device: Option<String>)
    where
        S: tokio::io::AsyncRead + tokio::io::AsyncWrite + Unpin + Send + 'static,
    {
        let paired = device.filter(|device| self.knows(device));
        if paired.is_none() && !self.pairing_is_open() {
            return;
        }
        let mut revoked = self.revoked.subscribe();
        let gateway = self.clone();
        let is_paired = paired.is_some();
        let service = service_fn(move |request| {
            let gateway = gateway.clone();
            async move { Ok::<_, hyper::Error>(gateway.answer(request, is_paired).await) }
        });
        let connection = hyper::server::conn::http1::Builder::new()
            .serve_connection(TokioIo::new(stream), service);
        tokio::pin!(connection);
        loop {
            tokio::select! {
                _ = connection.as_mut() => return,
                changed = revoked.changed() => {
                    if changed.is_err() || paired.as_ref().is_some_and(|d| !self.knows(d)) {
                        return;
                    }
                }
            }
        }
    }

    async fn answer(&self, request: Request<Incoming>, paired: bool) -> Reply {
        let path = request.uri().path().to_string();
        if !paired {
            return match (request.method(), path.as_str()) {
                (&Method::POST, "/pair/start") => {
                    self.pairing_step(request, |start| self.start_pairing(start))
                        .await
                }
                (&Method::POST, "/pair/finish") => {
                    self.pairing_step(request, |finish| self.finish_pairing(finish))
                        .await
                }
                _ => plain(StatusCode::NOT_FOUND),
            };
        }
        self.forward(request, &path).await
    }

    async fn pairing_step<B, A>(
        &self,
        request: Request<Incoming>,
        step: impl FnOnce(B) -> Option<A>,
    ) -> Reply
    where
        B: for<'de> Deserialize<'de>,
        A: Serialize,
    {
        let Ok(body) = Limited::new(request.into_body(), MAX_PAIRING_BODY)
            .collect()
            .await
        else {
            return plain(StatusCode::BAD_REQUEST);
        };
        let answer = serde_json::from_slice(&body.to_bytes())
            .ok()
            .and_then(step)
            .and_then(|answer| serde_json::to_vec(&answer).ok());
        match answer {
            Some(answer) => Response::new(boxed(answer)),
            None => plain(StatusCode::FORBIDDEN),
        }
    }

    async fn forward(&self, mut request: Request<Incoming>, path: &str) -> Reply {
        let Some((port, rest, token)) = self.route(path) else {
            return plain(StatusCode::NOT_FOUND);
        };
        let query = request
            .uri()
            .query()
            .map(|query| format!("?{query}"))
            .unwrap_or_default();
        let authority = format!("127.0.0.1:{port}");
        let Ok(uri) = format!("http://{authority}{rest}{query}").parse::<Uri>() else {
            return plain(StatusCode::BAD_REQUEST);
        };
        *request.uri_mut() = uri;
        let headers = request.headers_mut();
        headers.remove(ORIGIN);
        headers.remove(AUTHORIZATION);
        if let Ok(value) = authority.parse() {
            headers.insert(HOST, value);
        }
        if let Some(Ok(value)) = token.map(|token| format!("Bearer {token}").parse()) {
            headers.insert(AUTHORIZATION, value);
        }
        match self.client.request(request).await {
            Ok(response) => response.map(|body| body.boxed()),
            Err(_) => plain(StatusCode::BAD_GATEWAY),
        }
    }

    /// The port, the path there, and the token that service wants.
    fn route<'a>(&'a self, path: &'a str) -> Option<(u16, &'a str, Option<&'a str>)> {
        let under = |prefix: &str| {
            path.strip_prefix(prefix)
                .filter(|rest| rest.starts_with('/'))
        };
        let upstreams = &self.upstreams;
        if let Some(rest) = under("/host") {
            return Some((upstreams.host_port, rest, Some(&upstreams.host_token)));
        }
        if let Some(rest) = under("/coordinator") {
            return Some((upstreams.coordinator_port, rest, None));
        }
        under("/forgejo").map(|rest| (upstreams.forgejo_port, rest, None))
    }
}

type Reply = Response<BoxBody<Bytes, hyper::Error>>;

fn boxed(body: impl Into<Bytes>) -> BoxBody<Bytes, hyper::Error> {
    Full::new(body.into())
        .map_err(|never| match never {})
        .boxed()
}

fn plain(status: StatusCode) -> Reply {
    let mut response = Response::new(boxed(Bytes::new()));
    *response.status_mut() = status;
    response
}

fn pairing_code() -> Result<String, String> {
    let mut bytes = [0u8; 8];
    getrandom::fill(&mut bytes).map_err(|e| format!("make a pairing code: {e}"))?;
    Ok(bytes
        .iter()
        .map(|byte| CODE_ALPHABET[usize::from(byte % 32)] as char)
        .collect())
}

fn read_json<T: for<'de> Deserialize<'de>>(path: &Path) -> Result<Option<T>, String> {
    match fs::read(path) {
        Ok(body) => serde_json::from_slice(&body)
            .map(Some)
            .map_err(|_| format!("{} is not valid", path.display())),
        Err(error) if error.kind() == ErrorKind::NotFound => Ok(None),
        Err(error) => Err(format!("read {}: {error}", path.display())),
    }
}

/// Written to the side and moved in, so a crash never leaves half a file.
fn write_private<T: Serialize + ?Sized>(path: &Path, value: &T) -> Result<(), String> {
    let body = serde_json::to_vec_pretty(value).map_err(|e| e.to_string())?;
    let side = path.with_extension("new");
    match fs::remove_file(&side) {
        Err(error) if error.kind() != ErrorKind::NotFound => return Err(error.to_string()),
        _ => {}
    }
    OpenOptions::new()
        .write(true)
        .create_new(true)
        .mode(0o600)
        .open(&side)
        .and_then(|mut file| file.write_all(&body))
        .and_then(|()| fs::rename(&side, path))
        .map_err(|e| format!("write {}: {e}", path.display()))
}

/// Accepts any device that proves it holds its key. Which ones are paired is
/// the gateway's question, asked once the handshake is done.
#[derive(Debug)]
struct DeviceVerifier;

impl ClientCertVerifier for DeviceVerifier {
    fn root_hint_subjects(&self) -> &[DistinguishedName] {
        &[]
    }

    fn client_auth_mandatory(&self) -> bool {
        false
    }

    fn verify_client_cert(
        &self,
        _end_entity: &CertificateDer<'_>,
        _intermediates: &[CertificateDer<'_>],
        _now: UnixTime,
    ) -> Result<ClientCertVerified, rustls::Error> {
        Ok(ClientCertVerified::assertion())
    }

    fn verify_tls12_signature(
        &self,
        _message: &[u8],
        _cert: &CertificateDer<'_>,
        _dss: &DigitallySignedStruct,
    ) -> Result<rustls::client::danger::HandshakeSignatureValid, rustls::Error> {
        Err(rustls::Error::General("TLS 1.2 is not used".into()))
    }

    fn verify_tls13_signature(
        &self,
        message: &[u8],
        cert: &CertificateDer<'_>,
        dss: &DigitallySignedStruct,
    ) -> Result<rustls::client::danger::HandshakeSignatureValid, rustls::Error> {
        verify_tls13_signature(
            message,
            cert,
            dss,
            &provider().signature_verification_algorithms,
        )
    }

    fn supported_verify_schemes(&self) -> Vec<SignatureScheme> {
        vec![SignatureScheme::ED25519]
    }
}
