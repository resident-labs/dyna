package store

import (
	"fmt"
	"strings"
	"testing"
)

// A holder of several goals keeps the one it was started with; another with open items becomes an item of it, with those
// items as subtasks, and one with none ends done.
func TestUpgradeLeavesEachSessionOneGoal(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.writer.Exec(`DROP INDEX goals_holder; CREATE INDEX goals_holder ON goals(session) WHERE state='active'; ALTER TABLE goal_items DROP COLUMN parent; ALTER TABLE goals DROP COLUMN folded_goal; ALTER TABLE goals DROP COLUMN folded_item;
 INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0),('q','q',0,0,0);
 INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','r','s','p','open','own work',50),('t','q','t','p','open','',0);
 INSERT INTO goals(room,number,session,text,done_when,state,created_at,updated_at) VALUES
  ('r',1,'s','inherited','its end','active',10,10),('r',2,'s','own work','shipped','active',50,50),('r',3,'s','empty','x','active',60,60),('r',4,'s','old','x','done',5,5),
  ('q',1,'t','first','x','active',1,1),('q',2,'t','second','','active',2,2);
 INSERT INTO goal_items(room,goal,number,text,state,created_at,updated_at) VALUES
  ('r',1,1,'a','open',0,0),('r',1,2,'b','done',0,0),('r',1,3,'c','open',0,0),('r',2,7,'mine','open',0,0),('r',3,1,'gone','dropped',0,0),
  ('q',2,1,'z','open',0,0);
 ALTER TABLE workflow_runs DROP COLUMN paused; ALTER TABLE workflow_runs DROP COLUMN sub; ALTER TABLE workflow_runs DROP COLUMN key; DROP TABLE workflow_queue; DROP TABLE workflow_state; DROP TABLE workflow_subscriptions; ALTER TABLE room_repos DROP COLUMN pr_events; ALTER TABLE room_repos DROP COLUMN pr_census_at; UPDATE meta SET value='103' WHERE key='schema_version';`)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if s, err = Open(dir); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rows := func(q string) string {
		r, err := s.Read().Query(q)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		var out []string
		cols, _ := r.Columns()
		for r.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := r.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			var parts []string
			for _, v := range vals {
				parts = append(parts, fmt.Sprint(v))
			}
			out = append(out, strings.Join(parts, "|"))
		}
		return strings.Join(out, "\n")
	}
	if got, want := rows("SELECT room,number,state,summary,folded_goal,folded_item FROM goals ORDER BY room,number"), strings.Join([]string{
		"q|1|active||0|0",
		"q|2|dropped|folded into g1 as item 1 when a session came to hold one goal|1|1",
		"r|1|dropped|folded into g2 as item 8 when a session came to hold one goal|2|8",
		"r|2|active||0|0",
		"r|3|done|ended when a session came to hold one goal|0|0",
		"r|4|done||0|0",
	}, "\n"); got != want {
		t.Fatalf("goals:\n%s\nwant:\n%s", got, want)
	}
	if got, want := rows("SELECT room,goal,number,parent,text,state FROM goal_items WHERE goal IN (1,2) AND (room='q' AND goal=1 OR room='r' AND goal=2) ORDER BY room,number"), strings.Join([]string{
		"q|1|1|0|from g2: second|open",
		"q|1|2|1|z|open",
		"r|2|7|0|mine|open",
		"r|2|8|0|from g1: inherited — done when: its end|open",
		"r|2|9|8|a|open",
		"r|2|10|8|c|open",
	}, "\n"); got != want {
		t.Fatalf("items:\n%s\nwant:\n%s", got, want)
	}
	if _, err := s.writer.Exec("INSERT INTO goals(room,number,session,text,done_when,state,created_at,updated_at) VALUES('r',9,'s','again','x','active',0,0)"); err == nil {
		t.Fatal("a second active goal was stored")
	}
}
