//! The gateway over real TLS, against small stand-ins for what it forwards to.
#![cfg(unix)]

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
        },
    )
    .unwrap();
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let gateway_port = listener.local_addr().unwrap().port();
    tokio::spawn(gateway.clone().serve(listener));
    Fixture {
        gateway,
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
    };
    let first = Gateway::open(dir.path(), upstreams()).unwrap();
    let second = Gateway::open(dir.path(), upstreams()).unwrap();
    assert_eq!(first.fingerprint().unwrap(), second.fingerprint().unwrap());
}
