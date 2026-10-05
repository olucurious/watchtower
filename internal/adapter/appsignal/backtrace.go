package appsignal

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/olucurious/watchtower/internal/event"
)

// elixirLine matches Exception.format_stacktrace_entry output, e.g.
//
//	(library 0.4.1) lib/library/catalog.ex:42: Library.Catalog.fetch!/1
//	probe.exs:59: EndpointProbe.SyntheticFailure.raise_error/0
var elixirLine = regexp.MustCompile(`^(?:\(([\w.]+) [^)]*\) )?([^:\s][^:]*?)(?::(\d+))?: (.+)$`)

var mfa = regexp.MustCompile(`^(.+)\.([^.\s]+/\d+)$`)

// erlangCall matches location-less frames such as
// :erlang.map_get("...", %{"..." => integer()}).
var erlangCall = regexp.MustCompile(`^(:?[\w.]+)\.([\w!?]+)\(.*\)$`)

// frameworkApps are OTP and common dependency applications whose frames
// are never in-app. Frames from any other named application, or with no
// application prefix, are treated as the customer's code.
var frameworkApps = map[string]bool{
	"elixir": true, "stdlib": true, "kernel": true, "erts": true, "compiler": true,
	"logger": true, "iex": true, "mix": true, "eex": true, "ex_unit": true,
	"crypto": true, "ssl": true, "inets": true, "public_key": true,
	"phoenix": true, "phoenix_live_view": true, "plug": true, "plug_cowboy": true,
	"cowboy": true, "bandit": true, "thousand_island": true, "ranch": true,
	"ecto": true, "ecto_sql": true, "postgrex": true, "db_connection": true,
	"telemetry": true, "oban": true, "absinthe": true, "broadway": true, "gen_stage": true,
	"finch": true, "mint": true, "hackney": true, "tesla": true, "req": true,
	"jason": true, "appsignal": true, "sentry": true,
}

// parseBacktrace converts an integration's backtrace lines into frames in
// Sentry order (oldest call first). Elixir, Ruby and JavaScript list the
// innermost frame first; Python lists the oldest call first. Lines that
// cannot be parsed are kept as a frame whose function is the raw line.
func parseBacktrace(lang string, lines []string) []event.Frame {
	parse, oldestFirst := parseElixirLine, false
	switch lang {
	case "ruby":
		parse = parseRubyLine
	case "nodejs", "javascript":
		parse = parseJSLine
	case "python":
		parse, oldestFirst = parsePythonLine, true
	}
	frames := make([]event.Frame, 0, len(lines))
	for i := range lines {
		line := lines[i]
		if !oldestFirst {
			line = lines[len(lines)-1-i]
		}
		if f, ok := parseLine(parse, strings.TrimSpace(line)); ok {
			frames = append(frames, f)
		}
	}
	return frames
}

// parseLine applies a parser; a parser returning ok=false drops the line
// (e.g. the "Error: message" header of a V8 stack).
func parseLine(parse func(string) event.Frame, line string) (event.Frame, bool) {
	if line == "" || jsHeader.MatchString(line) {
		return event.Frame{}, false
	}
	return parse(line), true
}

// rubyLine matches "app/models/book.rb:12:in `reserve'" (Ruby ≤3.3) and
// "app/models/book.rb:12:in 'Library::Catalog.reserve'" (Ruby 3.4).
var rubyLine = regexp.MustCompile("^(.+?):(\\d+)(?::in [`'](.+)')?$")

func parseRubyLine(line string) event.Frame {
	m := rubyLine.FindStringSubmatch(line)
	if m == nil {
		return event.Frame{Function: line}
	}
	f := event.Frame{Filename: m[1], Function: m[3]}
	f.Lineno, _ = strconv.Atoi(m[2])
	if i := strings.LastIndexAny(f.Function, "#."); i > 0 && !strings.Contains(f.Function, " ") {
		f.Module, f.Function = f.Function[:i], f.Function[i+1:]
	}
	f.InApp = !strings.Contains(f.Filename, "/gems/") && !strings.Contains(f.Filename, "/lib/ruby/") &&
		!strings.HasPrefix(f.Filename, "<internal:")
	return f
}

var (
	// jsHeader is the "TypeError: message" first line of a V8 stack.
	jsHeader = regexp.MustCompile(`^[\w$.]*(Error|Exception)\b.*:|^[\w$.]*Error$`)
	// "at fn (file:1:2)", "at file:1:2", "at async fn (file:1:2)"
	v8Line = regexp.MustCompile(`^at (?:async )?(?:(.+?) \()?(.+?):(\d+):(\d+)\)?$`)
	// Firefox and Safari: "fn@file:1:2"
	geckoLine = regexp.MustCompile(`^(.*?)@(.+?):(\d+):(\d+)$`)
)

func parseJSLine(line string) event.Frame {
	m := v8Line.FindStringSubmatch(line)
	if m == nil {
		m = geckoLine.FindStringSubmatch(line)
	}
	if m == nil {
		return event.Frame{Function: line}
	}
	f := event.Frame{Function: m[1], Filename: m[2]}
	f.Lineno, _ = strconv.Atoi(m[3])
	f.Colno, _ = strconv.Atoi(m[4])
	if f.Function == "" {
		f.Function = "?"
	}
	f.InApp = !strings.Contains(f.Filename, "/node_modules/") && !strings.HasPrefix(f.Filename, "node:") &&
		f.Filename != "<anonymous>" && !strings.HasPrefix(f.Filename, "internal/")
	return f
}

// pythonLine matches traceback entries: File "app/x.py", line 12, in reserve
var pythonLine = regexp.MustCompile(`^File "(.+)", line (\d+), in (.+)$`)

func parsePythonLine(line string) event.Frame {
	m := pythonLine.FindStringSubmatch(line)
	if m == nil {
		return event.Frame{Function: line}
	}
	f := event.Frame{Filename: m[1], Function: m[3]}
	f.Lineno, _ = strconv.Atoi(m[2])
	f.InApp = !strings.Contains(f.Filename, "site-packages") && !strings.Contains(f.Filename, "dist-packages") &&
		!strings.Contains(f.Filename, "/lib/python3")
	return f
}

func parseElixirLine(line string) event.Frame {
	m := elixirLine.FindStringSubmatch(line)
	if m == nil {
		if c := erlangCall.FindStringSubmatch(line); c != nil {
			return event.Frame{Module: c[1], Function: c[2]}
		}
		return event.Frame{Function: line}
	}
	app, file, lineNo, fun := m[1], m[2], m[3], m[4]
	// Without an application prefix (scripts, umbrella apps in dev), a
	// path inside deps/ still marks a dependency.
	inApp := !frameworkApps[app] && !strings.Contains("/"+file, "/deps/")
	f := event.Frame{Filename: file, Function: fun, InApp: inApp}
	if n, err := strconv.Atoi(lineNo); err == nil {
		f.Lineno = n
	}
	target := fun
	if i := strings.Index(target, " in "); i >= 0 && strings.HasPrefix(target, "anonymous fn") {
		target = target[i+len(" in "):]
	}
	if mm := mfa.FindStringSubmatch(target); mm != nil {
		f.Module = mm[1]
		if target == fun {
			f.Function = mm[2]
		}
	}
	return f
}
