import type { Question } from "./contract.gen.ts";

// What a session said it does until the owner answers, or why it cannot. A question from before that was required has nothing to show.
export const questionPlan = ({ meanwhile, blocked }: Pick<Question, "meanwhile" | "blocked">) => meanwhile ? `${blocked ? "Blocked" : "Meanwhile"}: ${meanwhile}` : "";
