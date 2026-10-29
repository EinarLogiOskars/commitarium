// Window events the chat and the project's cards reload on. ChatView
// re-exports them, where most views import them from.

/** Fired when an approved assistant action changed the project's work. */
export const WORK_CHANGED = "commitarium:work-changed";
/** Asks the assistant to reload its conversation, as after autopilot starts or stops. */
export const ASSISTANT_CHANGED = "commitarium:assistant-changed";
/** Asks the open project to show its written plan (PLAN_MODE_PLAN.md PM6). */
export const OPEN_PLAN = "commitarium:open-plan";
/** Asks the planning chat to have the assistant write the plan's draft. */
export const WRITE_PLAN = "commitarium:write-plan";
