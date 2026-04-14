package relay

// Version is the relay binary version. Set at build time via:
//
//	go build -ldflags "-X github.com/lanby-dev/lanby-relay/internal/relay.Version=1.2.3"
//
// Falls back to "dev" when built without the flag.
var Version = "dev"
