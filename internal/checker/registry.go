package checker

import "fmt"

// Fields is the handoff shape a platform package's registered Factory
// receives -- a plain string-keyed map, not each platform's own typed
// Config struct. This is the key trick that lets callers (main.go) select
// a checker by name without importing every platform package
// unconditionally: a typed-Config-per-platform approach was considered and
// rejected specifically because main.go would still need to import
// ubuntu/debian/windows just to construct their literals, defeating the
// point of build-tag exclusion. See Config.CheckerFields (internal/config)
// for the translation from this project's own flat Config into this map.
type Fields map[string]string

// Factory constructs a Checker from Fields.
type Factory func(Fields) (Checker, error)

var registry = map[string]Factory{}

// Register adds name to the registry. Called from each platform
// package's own init() -- e.g. internal/checker/ubuntu registers
// "ubuntu". Registration only ever happens at program startup (init()
// functions run single-threaded before main), so this needs no locking.
//
// Panics on a duplicate name: that can only happen from a programming
// error (two packages registering the same name), never from user
// input, so failing loudly and immediately at startup is correct here --
// same posture as e.g. http.ServeMux panicking on a duplicate pattern.
func Register(name string, f Factory) {
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("checker: %q already registered", name))
	}
	registry[name] = f
}

// New constructs the Checker registered under name, using fields.
// Returns an error (not a panic) for an unknown name -- unlike a
// duplicate registration, this *can* legitimately happen from
// environment/build input (e.g. hostflavor.Detect returning a name no
// linked-in platform package registered).
func New(name string, fields Fields) (Checker, error) {
	f, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("checker: no checker registered for platform %q", name)
	}
	return f(fields)
}
