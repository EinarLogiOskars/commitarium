// Display vocabulary for the unit of work. The code and backend keep the term
// "feature"; the UI presents it as a "work order" (short: "order"). Swapping
// these strings re-labels the whole UI. See UI_PLAN.md → Naming.
export const WORK = {
  /** inline, lowercase — "this work order" */
  singular: "work order",
  /** heading / start of sentence — "Work order" */
  Singular: "Work order",
  /** inline, lowercase plural — "no work orders yet" */
  plural: "work orders",
  /** heading — "Work orders" */
  Plural: "Work orders",
  /** short inline form — "the order's goal" */
  short: "order",
  /** create-button label */
  newAction: "New order",
} as const;
