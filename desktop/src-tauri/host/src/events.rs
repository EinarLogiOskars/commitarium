//! Live updates from the host. One stream replaces Tauri's events, so a client
//! on another machine receives what a local one does. Each update is a
//! Server-Sent Events frame with a fixed name and a line of JSON. Code that
//! reports updates holds an `Events`, which does not care where they go. In
//! the host they go to a `Bus`, and the desktop app repeats them as Tauri
//! events, under the same names and with the same payloads.

use serde::Serialize;
use std::sync::mpsc::{channel, Receiver, Sender};
use std::sync::{Arc, Mutex};

/// Where updates go: `data` is the payload as a line of JSON.
pub trait Sink: Send + Sync + 'static {
    fn send(&self, name: &'static str, data: String);
}

/// A handle to report updates through. Cloning shares the same sink.
#[derive(Clone)]
pub struct Events(Arc<dyn Sink>);

impl Events {
    pub fn new(sink: impl Sink) -> Self {
        Self(Arc::new(sink))
    }

    pub fn publish(&self, name: &'static str, payload: &impl Serialize) {
        if let Ok(data) = serde_json::to_string(payload) {
            self.0.send(name, data);
        }
    }
}

/// Everyone listening to the host right now.
#[derive(Clone, Default)]
pub struct Bus {
    listeners: Arc<Mutex<Vec<Sender<String>>>>,
}

impl Bus {
    pub fn subscribe(&self) -> Receiver<String> {
        let (sender, receiver) = channel();
        if let Ok(mut listeners) = self.listeners.lock() {
            listeners.push(sender);
        }
        receiver
    }

    /// Sends to every listener and forgets the ones that have gone.
    pub fn publish(&self, name: &'static str, payload: &impl Serialize) {
        if let Ok(data) = serde_json::to_string(payload) {
            self.send(name, data);
        }
    }
}

impl Sink for Bus {
    fn send(&self, name: &'static str, data: String) {
        let frame = format!("event: {name}\ndata: {data}\n\n");
        if let Ok(mut listeners) = self.listeners.lock() {
            listeners.retain(|listener| listener.send(frame.clone()).is_ok());
        }
    }
}

/// Reads frames back from a stream that arrives in arbitrary pieces.
#[derive(Default)]
pub struct Frames {
    buffer: Vec<u8>,
}

impl Frames {
    /// Adds a piece of the stream and returns each `(name, data)` it finished.
    /// Comments, which keep the connection alive, have no name and are dropped.
    pub fn push(&mut self, bytes: &[u8]) -> Vec<(String, String)> {
        self.buffer.extend_from_slice(bytes);
        let mut frames = Vec::new();
        while let Some(end) = self.buffer.windows(2).position(|pair| pair == b"\n\n") {
            let frame: Vec<u8> = self.buffer.drain(..end + 2).collect();
            let frame = String::from_utf8_lossy(&frame);
            let name = frame.lines().find_map(|line| line.strip_prefix("event: "));
            let data = frame.lines().find_map(|line| line.strip_prefix("data: "));
            if let (Some(name), Some(data)) = (name, data) {
                frames.push((name.to_string(), data.to_string()));
            }
        }
        frames
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn every_listener_gets_the_update_and_gone_ones_are_forgotten() {
        let bus = Bus::default();
        let first = bus.subscribe();
        let second = bus.subscribe();
        drop(second);
        bus.publish("status", &vec!["ok"]);
        assert_eq!(first.recv().unwrap(), "event: status\ndata: [\"ok\"]\n\n");
        assert_eq!(bus.listeners.lock().unwrap().len(), 1);
    }

    #[test]
    fn events_reach_their_sink_as_one_line_of_json() {
        let bus = Bus::default();
        let listener = bus.subscribe();
        let events = Events::new(bus);
        events.clone().publish("status", &vec!["ok"]);
        assert_eq!(
            listener.recv().unwrap(),
            "event: status\ndata: [\"ok\"]\n\n"
        );
    }

    #[test]
    fn frames_are_read_across_pieces_and_comments_are_dropped() {
        let bus = Bus::default();
        let listener = bus.subscribe();
        bus.publish("status", &"a\nb");
        let frame = listener.recv().unwrap();
        let (start, end) = frame.as_bytes().split_at(9);

        let mut frames = Frames::default();
        assert!(frames.push(b": keep-alive\n\n").is_empty());
        assert!(frames.push(start).is_empty());
        assert_eq!(
            frames.push(end),
            vec![("status".to_string(), "\"a\\nb\"".to_string())]
        );
    }
}
