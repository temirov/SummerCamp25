// cmd/schedule/main.go
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"
)

// Session mirrors one element of sessions.json.
type Session struct {
	ID         int             `json:"id"`
	Activity   string          `json:"activity"`
	StartDate  string          `json:"startDate"`
	EndDate    string          `json:"endDate"`
	StartTime  string          `json:"startTime"`
	EndTime    string          `json:"endTime"`
	Days       []string        `json:"days"`
	Frequency  string          `json:"frequency"`
	Segment    string          `json:"segment"`
	PageURL    string          `json:"pageUrl"`
	Interested map[string]bool `json:"interested"`
	start      time.Time
	end        time.Time
}

func mustParse(layout, v string) time.Time {
	t, err := time.Parse(layout, v)
	if err != nil {
		panic(err)
	}
	return t
}
func parseClock(c string) time.Duration {
	t := mustParse("3:04 PM", c)
	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute
}

func loadSessions() []Session {
	b, _ := os.ReadFile("sessions.json")
	var s []Session
	_ = json.Unmarshal(b, &s)
	for i := range s {
		s[i].ID = i
		day := mustParse("January 2, 2006", s[i].StartDate)
		s[i].start = day.Add(parseClock(s[i].StartTime))
		s[i].end = day.Add(parseClock(s[i].EndTime))
	}
	sort.Slice(s, func(i, j int) bool { return s[i].start.Before(s[j].start) })
	return s
}

type kidPlan struct {
	name       string
	chosen     []Session
	busy       []Session
	activities map[string]struct{}
}

func (p *kidPlan) canAdd(s Session) bool {
	if _, dup := p.activities[s.Activity]; dup {
		return false
	}
	for _, b := range p.busy {
		if !(s.end.Before(b.start) || b.end.Before(s.start)) {
			return false
		}
	}
	return true
}
func (p *kidPlan) add(s Session) {
	p.chosen = append(p.chosen, s)
	p.busy = append(p.busy, s)
	p.activities[s.Activity] = struct{}{}
}

func earliest(list []Session) Session { return list[0] }

func main() {
	sessions := loadSessions()
	kids := []string{"Alice", "Peter"}

	plans := map[string]*kidPlan{}
	for _, k := range kids {
		plans[k] = &kidPlan{name: k, activities: map[string]struct{}{}}
	}

	byActivity := map[string][]Session{}
	for _, s := range sessions {
		byActivity[s.Activity] = append(byActivity[s.Activity], s)
	}

	jointSet := map[string]struct{}{}
	for act, list := range byActivity {
		s := earliest(list)
		if s.Interested["Alice"] && s.Interested["Peter"] {
			if plans["Alice"].canAdd(s) && plans["Peter"].canAdd(s) {
				for _, k := range kids {
					plans[k].add(s)
				}
				jointSet[act] = struct{}{}
				delete(byActivity, act)
			}
		}
	}

	for _, kid := range kids {
		for act, list := range byActivity {
			s := earliest(list)
			if s.Interested[kid] && plans[kid].canAdd(s) {
				plans[kid].add(s)
				delete(byActivity, act)
			}
		}
	}

	for _, p := range plans {
		sort.Slice(p.chosen, func(i, j int) bool { return p.chosen[i].start.Before(p.chosen[j].start) })
	}

	var joint []Session
	for _, s := range plans["Alice"].chosen {
		if _, ok := jointSet[s.Activity]; ok {
			joint = append(joint, s)
		}
	}
	sort.Slice(joint, func(i, j int) bool { return joint[i].start.Before(joint[j].start) })

	row := func(s Session) string {
		return fmt.Sprintf(
			"%s | %s | %s | %s | %s–%s | %s",
			s.StartDate, s.EndDate, s.Activity, s.Frequency, s.StartTime, s.EndTime, s.PageURL)
	}

	fmt.Println("== Joint (both kids) ==")
	for _, s := range joint {
		fmt.Println(row(s))
	}
	fmt.Println()

	for _, kid := range kids {
		fmt.Printf("== %s ==\n", kid)
		for _, s := range plans[kid].chosen {
			if _, jointAct := jointSet[s.Activity]; jointAct {
				continue
			}
			fmt.Println(row(s))
		}
		fmt.Println()
	}
}
