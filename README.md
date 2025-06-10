# SummerCamp ’24 planner 🏕️

Small weekend-project that helps us:

* **scrape** every camp / class date that shows up on the City of Manhattan Beach
  ActiveNet site
* **merge + clean** the raw JSON into one canonical `sessions.json`
* **visualise** all sessions on an interactive timeline
* **optimise** Alice & Peter’s personal schedule so the two never overlap and
  each child attends at most one session of every activity

<br>

| folder / file        | purpose                                                                   |
|----------------------|---------------------------------------------------------------------------|
| `scrapes/…/*.json`   | one file per search-results page – produced by the browser helper         |
| `browser_helper.js`  | 30-line bookmarklet you run inside Chrome Dev-Tools to create the above   |
| `cmd/merge/merge.go` | turns the raw dumps into `sessions.json` (with validation & logs)         |
| `sessions.json`      | single machine-readable truth-file (input for timeline & solver)          |
| `timeline.html`      | open in a browser → interactive colour-coded Gantt                        |
| `main.go`            | CP-SAT model that picks the **max #** of non-overlapping sessions per kid |

---

## 1 Scrape

1. open **any** result list on  
   <https://anc.apm.activecommunities.com/citymb/activity/search>
2. paste the entire contents of **`browser_helper.js`** into the _Console_  
   → clipboard now contains a fresh `[…]` JSON array
3. save it as e.g. `scrapes/2025-08-field-trip.json`

_No defaults, no guessing – rows missing a date-range **or** a parseable
time-span are simply skipped._

---

## 2 Merge & validation

```bash
go run ./cmd/merge        # emits sessions.json
````

The script

* keeps only titles listed in the **`allowed`** map
  (that map also stores who may attend)
* normalises

    * “Noon” → `12:00 PM`
    * “Aug 5 – Aug 26 2025” → separate `startDate` / `endDate`
* rejects unparseable rows and prints a ⚠️ log for every drop
* NEVER HTML-escapes – real `&` in `pageUrl`, not `\u0026`.

---

## 3 Timeline

Simply open **`timeline.html`** in a browser – no server required.

Colour legend (top-left)

| swatch | slot      |
|--------|-----------|
| green  | all-day   |
| yellow | morning   |
| blue   | afternoon |

Each bar links back to the original ActiveNet page (tooltip → *source*).

---

## 4 Schedule optimiser (optional)

```bash
go run main.go
```

The CP-SAT model:

1. **one session per activity per child**
2. **no overlaps on the same day** (30 min travel buffer)
3. maximise the total number of chosen sessions

Output is a plain ASCII table:

```
| Date        | Alice (chosen)                         | Peter (chosen)                         |
|-------------|----------------------------------------|----------------------------------------|
| Mon 22 Jul  | Camp Clay, Paint and Draw 9:00–12:00   | Rocket Science & Astronomy! 9:00–12:00 |
...
```

---

## Prerequisites

* Go 1.21+
* *only for the solver* – **OR-Tools** `go get github.com/google/or-tools/sat`

That’s it – happy planning! 🎉
