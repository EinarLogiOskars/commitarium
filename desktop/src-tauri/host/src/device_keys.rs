//! What a device and a host need to trust each other (ADR-040): an Ed25519
//! identity each, pinned by certificate fingerprint at pairing, and the SPAKE2
//! exchange that makes pairing safe over a connection neither side trusts yet.
//! The host's side of pairing is in `gateway.rs`; `pair` is the device's.

use base64::{engine::general_purpose::STANDARD as BASE64, Engine};
use hmac::{Hmac, Mac};
use rustls::client::danger::{HandshakeSignatureValid, ServerCertVerified, ServerCertVerifier};
use rustls::crypto::{ring, verify_tls13_signature, CryptoProvider};
use rustls::pki_types::{CertificateDer, PrivateKeyDer, ServerName, UnixTime};
use rustls::{ClientConfig, DigitallySignedStruct, SignatureScheme};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use spake2::{Ed25519Group, Identity as PakeIdentity, Password, Spake2};
use std::sync::{Arc, Mutex};
use std::time::Duration;

pub(crate) const DEVICE_SIDE: &[u8] = b"commitarium device";
pub(crate) const HOST_SIDE: &[u8] = b"commitarium host";
/// Where a device asks to be connected to a preview's port.
pub(crate) const TUNNEL_PREFIX: &str = "/tunnel/";
pub(crate) const TUNNEL_PROTOCOL: &str = "commitarium-tunnel";

/// A self-signed certificate and its private key, both base64 DER.
#[derive(Clone, Deserialize, Serialize)]
pub struct Identity {
    pub certificate: String,
    pub key: String,
}

impl Identity {
    pub fn generate() -> Result<Self, String> {
        let key = rcgen::KeyPair::generate_for(&rcgen::PKCS_ED25519)
            .map_err(|e| format!("make a key: {e}"))?;
        let certificate = rcgen::CertificateParams::new(vec!["commitarium".to_string()])
            .and_then(|params| params.self_signed(&key))
            .map_err(|e| format!("make a certificate: {e}"))?;
        Ok(Self {
            certificate: BASE64.encode(certificate.der()),
            key: BASE64.encode(key.serialize_der()),
        })
    }

    pub fn fingerprint(&self) -> Result<String, String> {
        Ok(fingerprint(&self.certificate_der()?))
    }

    pub fn certificate_der(&self) -> Result<CertificateDer<'static>, String> {
        BASE64
            .decode(&self.certificate)
            .map(CertificateDer::from)
            .map_err(|_| "the identity's certificate is not valid".to_string())
    }

    pub fn key_der(&self) -> Result<PrivateKeyDer<'static>, String> {
        let der = BASE64
            .decode(&self.key)
            .map_err(|_| "the identity's key is not valid".to_string())?;
        PrivateKeyDer::try_from(der).map_err(|_| "the identity's key is not valid".to_string())
    }
}

pub fn fingerprint(certificate: &[u8]) -> String {
    Sha256::digest(certificate)
        .iter()
        .map(|byte| format!("{byte:02x}"))
        .collect()
}

pub(crate) fn provider() -> Arc<CryptoProvider> {
    Arc::new(ring::default_provider())
}

/// Ties both fingerprints to the key only someone with the code can have.
pub(crate) fn proof(key: &[u8], side: &[u8], host: &str, device: &str) -> Vec<u8> {
    proof_mac(key, side, host, device)
        .finalize()
        .into_bytes()
        .to_vec()
}

pub(crate) fn proof_matches(
    key: &[u8],
    side: &[u8],
    host: &str,
    device: &str,
    seen: &[u8],
) -> bool {
    proof_mac(key, side, host, device)
        .verify_slice(seen)
        .is_ok()
}

fn proof_mac(key: &[u8], side: &[u8], host: &str, device: &str) -> Hmac<Sha256> {
    let mut mac = Hmac::<Sha256>::new_from_slice(key).expect("HMAC takes a key of any length");
    for part in [side, host.as_bytes(), device.as_bytes()] {
        mac.update(part);
    }
    mac
}

fn pake_sides() -> (PakeIdentity, PakeIdentity) {
    (PakeIdentity::new(DEVICE_SIDE), PakeIdentity::new(HOST_SIDE))
}

/// The host's answer to a device's first message: its own, and the shared key.
pub(crate) fn pake_answer(code: &str, message: &[u8]) -> Result<(Vec<u8>, Vec<u8>), String> {
    let (device, host) = pake_sides();
    let (state, answer) =
        Spake2::<Ed25519Group>::start_b(&Password::new(code.as_bytes()), &device, &host);
    let key = state
        .finish(message)
        .map_err(|_| "the pairing message is not valid".to_string())?;
    Ok((answer, key))
}

#[derive(Deserialize, Serialize)]
pub(crate) struct PairStart {
    pub message: String,
}

#[derive(Deserialize, Serialize)]
pub(crate) struct PairFinish {
    pub name: String,
    pub certificate: String,
    pub proof: String,
}

#[derive(Deserialize, Serialize)]
pub(crate) struct PairDone {
    pub proof: String,
}

/// A paired host, as a device keeps it.
#[derive(Clone, Debug, Deserialize, PartialEq, Serialize)]
pub struct PairedHost {
    pub address: String,
    pub fingerprint: String,
}

/// What `commitarium-host pair` prints, taken apart.
pub fn parse_pairing_line(line: &str) -> Result<(String, String), String> {
    line.trim()
        .strip_prefix("commitarium://")
        .and_then(|rest| rest.split_once('#'))
        .filter(|(address, code)| !address.is_empty() && !code.is_empty())
        .map(|(address, code)| (address.to_string(), code.to_string()))
        .ok_or_else(|| "that does not look like a pairing line".to_string())
}

/// Pairs this device with the host in `line`. The host's certificate is not
/// trusted here: the exchange proves it, or pairing fails.
pub async fn pair(line: &str, name: &str, device: &Identity) -> Result<PairedHost, String> {
    let (address, code) = parse_pairing_line(line)?;
    let seen = Arc::new(Mutex::new(None));
    let client = reqwest_client(Pin::Record(seen.clone()), None)?;
    let base = format!("https://{address}");

    let (sides_device, sides_host) = pake_sides();
    let (state, message) = Spake2::<Ed25519Group>::start_a(
        &Password::new(code.as_bytes()),
        &sides_device,
        &sides_host,
    );
    let answer: PairStart = post(
        &client,
        &format!("{base}/pair/start"),
        &PairStart {
            message: BASE64.encode(message),
        },
    )
    .await?;
    let answer = BASE64
        .decode(answer.message)
        .map_err(|_| "the host sent an answer that could not be read".to_string())?;
    let key = state.finish(&answer).map_err(|_| refused())?;

    let host_fingerprint = seen
        .lock()
        .expect("the fingerprint lock is never poisoned")
        .clone()
        .ok_or_else(refused)?;
    let device_fingerprint = device.fingerprint()?;
    let done: PairDone = post(
        &client,
        &format!("{base}/pair/finish"),
        &PairFinish {
            name: name.to_string(),
            certificate: device.certificate.clone(),
            proof: BASE64.encode(proof(
                &key,
                DEVICE_SIDE,
                &host_fingerprint,
                &device_fingerprint,
            )),
        },
    )
    .await?;
    let host_proof = BASE64.decode(done.proof).map_err(|_| refused())?;
    if !proof_matches(
        &key,
        HOST_SIDE,
        &host_fingerprint,
        &device_fingerprint,
        &host_proof,
    ) {
        return Err(refused());
    }
    Ok(PairedHost {
        address,
        fingerprint: host_fingerprint,
    })
}

fn refused() -> String {
    "The code did not work: it lasts 5 minutes and works once. On the server, run \
     sudo commitarium-host pair for a new one."
        .to_string()
}

async fn post<B: Serialize, A: for<'de> Deserialize<'de>>(
    client: &reqwest::Client,
    url: &str,
    body: &B,
) -> Result<A, String> {
    // A server whose pairing has closed answers the connection, then drops it.
    let answer = client.post(url).json(body).send().await.map_err(|error| {
        if error.is_connect() || error.is_timeout() {
            "Could not reach the server. Check its address and firewall.".to_string()
        } else {
            refused()
        }
    })?;
    if !answer.status().is_success() {
        return Err(refused());
    }
    answer.json().await.map_err(|_| refused())
}

/// A client for a paired host: it presents this device and accepts only the
/// host pinned at pairing, whatever address it is at.
pub fn host_client(device: &Identity, host_fingerprint: &str) -> Result<reqwest::Client, String> {
    reqwest_client(Pin::Exactly(host_fingerprint.to_string()), Some(device))
}

fn reqwest_client(pin: Pin, device: Option<&Identity>) -> Result<reqwest::Client, String> {
    let builder = ClientConfig::builder_with_provider(provider())
        .with_protocol_versions(&[&rustls::version::TLS13])
        .map_err(|e| e.to_string())?
        .dangerous()
        .with_custom_certificate_verifier(Arc::new(HostVerifier { pin }));
    let config = match device {
        Some(device) => builder
            .with_client_auth_cert(vec![device.certificate_der()?], device.key_der()?)
            .map_err(|e| e.to_string())?,
        None => builder.with_no_client_auth(),
    };
    reqwest::Client::builder()
        .no_proxy()
        .connect_timeout(CONNECT_PATIENCE)
        .tcp_keepalive(KEEP_ALIVE)
        .http1_only()
        .use_preconfigured_tls(config)
        .build()
        .map_err(|e| e.to_string())
}

const CONNECT_PATIENCE: Duration = Duration::from_secs(10);
/// Notices a network that went away during a long, quiet stream.
const KEEP_ALIVE: Duration = Duration::from_secs(20);

#[derive(Debug)]
enum Pin {
    /// Pairing: nothing is known yet, so remember what was shown.
    Record(Arc<Mutex<Option<String>>>),
    Exactly(String),
}

/// Trusts a pinned fingerprint, not a certificate authority or a name.
#[derive(Debug)]
struct HostVerifier {
    pin: Pin,
}

impl ServerCertVerifier for HostVerifier {
    fn verify_server_cert(
        &self,
        end_entity: &CertificateDer<'_>,
        _intermediates: &[CertificateDer<'_>],
        _server_name: &ServerName<'_>,
        _ocsp_response: &[u8],
        _now: UnixTime,
    ) -> Result<ServerCertVerified, rustls::Error> {
        let shown = fingerprint(end_entity);
        match &self.pin {
            Pin::Record(seen) => {
                *seen.lock().expect("the fingerprint lock is never poisoned") = Some(shown);
                Ok(ServerCertVerified::assertion())
            }
            Pin::Exactly(pinned) if *pinned == shown => Ok(ServerCertVerified::assertion()),
            Pin::Exactly(_) => Err(rustls::Error::General("not the paired host".into())),
        }
    }

    fn verify_tls12_signature(
        &self,
        _message: &[u8],
        _cert: &CertificateDer<'_>,
        _dss: &DigitallySignedStruct,
    ) -> Result<HandshakeSignatureValid, rustls::Error> {
        Err(rustls::Error::General("TLS 1.2 is not used".into()))
    }

    fn verify_tls13_signature(
        &self,
        message: &[u8],
        cert: &CertificateDer<'_>,
        dss: &DigitallySignedStruct,
    ) -> Result<HandshakeSignatureValid, rustls::Error> {
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_pairing_line_comes_apart() {
        assert_eq!(
            parse_pairing_line(" commitarium://10.0.0.1:7443#ABCD2345\n").unwrap(),
            ("10.0.0.1:7443".to_string(), "ABCD2345".to_string())
        );
        assert!(parse_pairing_line("https://10.0.0.1").is_err());
        assert!(parse_pairing_line("commitarium://10.0.0.1").is_err());
    }

    #[test]
    fn a_proof_covers_both_fingerprints_and_the_side() {
        let made = proof(b"key", DEVICE_SIDE, "host", "device");
        assert!(proof_matches(b"key", DEVICE_SIDE, "host", "device", &made));
        assert!(!proof_matches(b"key", HOST_SIDE, "host", "device", &made));
        assert!(!proof_matches(b"key", DEVICE_SIDE, "fake", "device", &made));
        assert!(!proof_matches(
            b"other",
            DEVICE_SIDE,
            "host",
            "device",
            &made
        ));
    }

    #[test]
    fn identities_differ_and_keep_their_fingerprint() {
        let one = Identity::generate().unwrap();
        let two = Identity::generate().unwrap();
        assert_ne!(one.fingerprint().unwrap(), two.fingerprint().unwrap());
        assert_eq!(
            one.fingerprint().unwrap(),
            one.clone().fingerprint().unwrap()
        );
        one.key_der().unwrap();
    }
}
