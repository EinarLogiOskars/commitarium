//! The host's async runtime. Library code can run in any process and on any
//! thread, so it never relies on one being current: this is a shared
//! multi-threaded tokio runtime, built on first use. A process that never asks
//! for it never starts it. As with any tokio runtime, `block_on` panics on a
//! thread already inside a runtime's async context.

use std::future::Future;
use std::sync::OnceLock;
use tokio::runtime::Runtime;
use tokio::task::JoinHandle;

fn runtime() -> &'static Runtime {
    static RUNTIME: OnceLock<Runtime> = OnceLock::new();
    RUNTIME.get_or_init(|| Runtime::new().expect("start the host's async runtime"))
}

pub fn block_on<F: Future>(future: F) -> F::Output {
    runtime().block_on(future)
}

pub fn spawn<F>(future: F) -> JoinHandle<F::Output>
where
    F: Future + Send + 'static,
    F::Output: Send + 'static,
{
    runtime().spawn(future)
}

pub fn spawn_blocking<F, R>(work: F) -> JoinHandle<R>
where
    F: FnOnce() -> R + Send + 'static,
    R: Send + 'static,
{
    runtime().spawn_blocking(work)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn works_from_a_thread_outside_any_runtime() {
        assert_eq!(block_on(async { 1 }), 1);
        assert_eq!(block_on(spawn(async { 2 })).unwrap(), 2);
        assert_eq!(block_on(spawn_blocking(|| 3)).unwrap(), 3);
    }

    #[test]
    fn blocking_work_can_block_on_the_runtime() {
        let joined = block_on(spawn_blocking(|| block_on(async { 4 })));
        assert_eq!(joined.unwrap(), 4);
    }

    #[test]
    fn a_task_can_be_awaited_from_another_runtime() {
        let other = Runtime::new().unwrap();
        assert_eq!(other.block_on(spawn_blocking(|| 5)).unwrap(), 5);
    }
}
