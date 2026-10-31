import { test } from "node:test";
import assert from "node:assert/strict";
import { FormDrafts } from "./form-drafts.ts";

test("an unfocused edited form keeps a reload from losing its draft", () => {
  const forms = new FormDrafts();
  const control = { type: "text", value: "old", isConnected: true };
  forms.focus(control);
  control.value = "new";
  forms.input(control);
  assert.equal(forms.pending, true);
  control.value = "old";
  forms.input(control);
  assert.equal(forms.pending, false);
  const scope = { type: "checkbox", value: "on", checked: false, isConnected: true };
  forms.focus(scope);
  scope.checked = true;
  forms.input(scope);
  assert.equal(forms.pending, true);
  scope.checked = false;
  forms.input(scope);
  assert.equal(forms.pending, false);
});
