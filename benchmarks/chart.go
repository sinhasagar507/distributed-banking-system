// Command chart renders benchmarks/results.csv's instances=1/2/3 sweep rows
// (IMPROVEMENT_PLAN §3.4) as an SVG line chart: p95 latency vs. app-instance
// count, one line per scenario. Pure stdlib — no matplotlib/gnuplot
// dependency, since neither was available in the environment that ran the
// sweep.
//
// Usage: go run benchmarks/chart.go
package main

import (
	"encoding/csv"
	"fmt"
	"os"
	"sort"
)

type point struct {
	instances int
	p95       float64
}

func main() {
	f, err := os.Open("benchmarks/results.csv")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open results.csv:", err)
		os.Exit(1)
	}
	defer f.Close()

	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		fmt.Fprintln(os.Stderr, "read results.csv:", err)
		os.Exit(1)
	}
	if len(rows) < 2 {
		fmt.Fprintln(os.Stderr, "no data rows in results.csv")
		os.Exit(1)
	}
	header := rows[0]
	col := map[string]int{}
	for i, h := range header {
		col[h] = i
	}

	series := map[string][]point{}
	for _, r := range rows[1:] {
		instances := 0
		fmt.Sscanf(r[col["instances"]], "%d", &instances)
		if instances == 0 {
			continue // informational-only rows predating the §3.4 sweep (instances=0)
		}
		var p95 float64
		fmt.Sscanf(r[col["p95_ms"]], "%f", &p95)
		scenario := r[col["scenario"]]
		series[scenario] = append(series[scenario], point{instances, p95})
	}

	scenarios := make([]string, 0, len(series))
	for s := range series {
		scenarios = append(scenarios, s)
		sort.Slice(series[s], func(i, j int) bool { return series[s][i].instances < series[s][j].instances })
	}
	sort.Strings(scenarios)

	const (
		width, height = 640, 420
		padL, padB    = 70, 50
		padT, padR    = 30, 30
	)
	plotW := width - padL - padR
	plotH := height - padT - padB

	maxP95 := 0.0
	for _, pts := range series {
		for _, p := range pts {
			if p.p95 > maxP95 {
				maxP95 = p.p95
			}
		}
	}
	maxP95 *= 1.1 // headroom

	colors := map[string]string{"login": "#d62728", "transaction": "#1f77b4", "monthly": "#2ca02c"}

	x := func(instances int) float64 {
		return float64(padL) + float64(instances-1)/2.0*float64(plotW)
	}
	y := func(p95 float64) float64 {
		return float64(padT) + float64(plotH) - (p95/maxP95)*float64(plotH)
	}

	svg := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" font-family="sans-serif" font-size="12">`+"\n", width, height, width, height)
	svg += fmt.Sprintf(`<rect width="%d" height="%d" fill="white"/>`+"\n", width, height)
	svg += `<text x="320" y="20" text-anchor="middle" font-size="15" font-weight="bold">DISBank: p95 latency vs. app-instance count (IMPROVEMENT_PLAN 3.4)</text>` + "\n"

	// axes
	svg += fmt.Sprintf(`<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="black"/>`+"\n", padL, padT, padL, padT+plotH)
	svg += fmt.Sprintf(`<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="black"/>`+"\n", padL, padT+plotH, padL+plotW, padT+plotH)
	for _, inst := range []int{1, 2, 3} {
		svg += fmt.Sprintf(`<text x="%.0f" y="%d" text-anchor="middle">%d</text>`+"\n", x(inst), padT+plotH+20, inst)
	}
	svg += fmt.Sprintf(`<text x="%d" y="%d" text-anchor="middle">app instances</text>`+"\n", padL+plotW/2, height-10)
	for i := 0; i <= 4; i++ {
		v := maxP95 * float64(i) / 4
		yy := y(v)
		svg += fmt.Sprintf(`<text x="%d" y="%.0f" text-anchor="end">%.0f</text>`+"\n", padL-8, yy+4, v)
		svg += fmt.Sprintf(`<line x1="%d" y1="%.0f" x2="%d" y2="%.0f" stroke="#eee"/>`+"\n", padL, yy, padL+plotW, yy)
	}
	svg += fmt.Sprintf(`<text x="20" y="%d" transform="rotate(-90 20 %d)" text-anchor="middle">p95 ms</text>`+"\n", padT+plotH/2, padT+plotH/2)

	legendY := padT
	for _, s := range scenarios {
		pts := series[s]
		c := colors[s]
		path := ""
		for i, p := range pts {
			cmd := "L"
			if i == 0 {
				cmd = "M"
			}
			path += fmt.Sprintf("%s%.1f,%.1f ", cmd, x(p.instances), y(p.p95))
		}
		svg += fmt.Sprintf(`<path d="%s" fill="none" stroke="%s" stroke-width="2"/>`+"\n", path, c)
		for _, p := range pts {
			svg += fmt.Sprintf(`<circle cx="%.1f" cy="%.1f" r="3" fill="%s"/>`+"\n", x(p.instances), y(p.p95), c)
		}
		svg += fmt.Sprintf(`<rect x="%d" y="%d" width="10" height="10" fill="%s"/><text x="%d" y="%d">%s</text>`+"\n",
			padL+plotW-120, legendY, c, padL+plotW-105, legendY+9, s)
		legendY += 16
	}

	svg += `</svg>` + "\n"

	if err := os.WriteFile("benchmarks/scaling.svg", []byte(svg), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "write scaling.svg:", err)
		os.Exit(1)
	}
	fmt.Println("wrote benchmarks/scaling.svg")
}
