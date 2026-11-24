package tools

import "sync"

// Tool sources reported by Check.Source.
const (
	SourceBuiltin = "builtin"
	SourceSystem  = "system"
	SourceCustom  = "custom"
)

// Check is the result of probing one tool. Path/Version describe the binary
// the app will actually run; BuiltinPath/BuiltinVersion describe the copy that
// shipped with the app, even when a newer system copy won.
type Check struct {
	Name           string
	Path           string
	Version        string
	OK             bool
	TimedOut       bool
	BuiltinPath    string
	BuiltinVersion string
	OverridePath   string
	OverrideOK     bool
	Source         string // SourceBuiltin, SourceSystem, or SourceCustom
}

// ProbeAll checks the tools the download pipeline depends on. yt-dlp can take
// several seconds to answer on a cold start, so the probes run concurrently.
func ProbeAll() []Check {
	names := []string{"yt-dlp", "ffmpeg", "ffprobe"}
	checks := make([]Check, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			checks[i] = Probe(name)
		}(i, name)
	}
	wg.Wait()
	return checks
}

// Probe resolves a tool and reads its version, reporting whether it runs and
// where it came from.
func Probe(name string) Check {
	check := Check{Name: name, BuiltinPath: bundledPath(name)}
	if check.BuiltinPath != "" {
		check.BuiltinVersion = probeVersion(check.BuiltinPath, name)
	}
	if override := overrideFor(name); override != "" {
		check.OverridePath = override
		check.OverrideOK = usable(override, name)
	}

	check.Path = Resolve(name)
	if check.Path == "" {
		// Keep a timeout visible even when no candidate passed the probe.
		if check.BuiltinPath != "" && probe(check.BuiltinPath, name).timedOut {
			check.TimedOut = true
		}
		for _, candidate := range installedCandidates(name) {
			if probe(candidate, name).timedOut {
				check.TimedOut = true
				break
			}
		}
		// The service keeps a present-but-unverified binary as its fallback;
		// show that path and mark it unavailable rather than pretending it is
		// missing.
		switch {
		case check.BuiltinPath != "":
			check.Path = check.BuiltinPath
			check.Source = SourceBuiltin
		default:
			for _, candidate := range installedCandidates(name) {
				if fileExists(candidate) {
					check.Path = candidate
					check.Source = SourceSystem
					break
				}
			}
		}
		return check
	}
	result := probe(check.Path, name)
	check.Version = result.version
	check.OK = result.ok
	check.TimedOut = result.timedOut
	switch {
	case check.OverrideOK && check.Path == check.OverridePath:
		check.Source = SourceCustom
	case check.BuiltinPath != "" && check.Path == check.BuiltinPath:
		check.Source = SourceBuiltin
	default:
		check.Source = SourceSystem
	}
	return check
}
