import { test } from "node:test";
import assert from "node:assert/strict";
import { canForwardPost, replaceForward, undoForward } from "./forward-state.ts";

test("replace and Undo restore the previous post; failed posts never forward", () => {
  const first = { author: "A", at: "1", text: "one", attachments: [] };
  const second = { author: "B", at: "2", text: "two", attachments: [] };
  const state = { quote: first };
  assert.equal(replaceForward(state, first), state);
  const replaced = replaceForward(state, second);
  assert.deepEqual(replaced, { quote: second, previous: first });
  assert.deepEqual(undoForward(replaced), { quote: first });
  assert.equal(canForwardPost(true, false, "failed", 0), false);
  assert.equal(canForwardPost(false, true, "waiting", 0), false);
  assert.equal(canForwardPost(false, false, "", 0), false);
  assert.equal(canForwardPost(false, false, "", 1), true);
});

test("data-only forwards remain eligible and preserve complete data through replace/Undo", () => {
 const first = {author:"crew",at:"1",text:"",attachments:[],data:"null"};
 const second = {author:"crew",at:"2",text:"summary",attachments:[],data:'{"n":9007199254740993,"e":1e0,"groups":[{"conflicts":["a.go","b.go"]}]}'};
 const next=replaceForward({quote:first},second);
 assert.deepEqual(next.quote?.data,second.data);
 assert.equal(undoForward(next).quote?.data,"null");
 const wire=JSON.parse(JSON.stringify(next.quote));assert.equal(wire.data,second.data,"quote wire preserves numeric token spellings");
 assert.equal(canForwardPost(false,false,"",0,true),true);
 assert.equal(canForwardPost(true,false,"",0,true),false);
});
