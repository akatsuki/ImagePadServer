use std::time::{Duration, Instant};

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum State {
    Starting,
    Ready,
    Stopping,
    Stopped,
    Crashed,
}

#[derive(Debug)]
pub struct Lifecycle {
    state: State,
    last_heartbeat: Instant,
    timeout: Duration,
}
impl Lifecycle {
    pub fn new(timeout: Duration) -> Self {
        Self {
            state: State::Starting,
            last_heartbeat: Instant::now(),
            timeout,
        }
    }
    pub fn ready(&mut self) {
        if self.state == State::Starting {
            self.state = State::Ready;
            self.beat();
        }
    }
    pub fn beat(&mut self) {
        self.last_heartbeat = Instant::now();
    }
    pub fn expired(&self) -> bool {
        self.state == State::Ready && self.last_heartbeat.elapsed() > self.timeout
    }
    pub fn shutdown(&mut self) {
        if matches!(self.state, State::Starting | State::Ready) {
            self.state = State::Stopping;
        }
    }
    pub fn stopped(&mut self) {
        self.state = State::Stopped;
    }
    pub fn crashed(&mut self) {
        self.state = State::Crashed;
    }
    pub fn state(&self) -> State {
        self.state
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn shutdown_is_ordered() {
        let mut l = Lifecycle::new(Duration::from_secs(1));
        l.ready();
        l.shutdown();
        assert_eq!(l.state(), State::Stopping);
        l.stopped();
        assert_eq!(l.state(), State::Stopped);
    }
}
