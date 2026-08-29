// The sidecar links: everything this service says to a sidecar goes through
// one injected function. The host owns the relay, its socket and its token —
// this module never dials. There is no Stream: the surface path carries no
// byte stream (the render sidecar displays; the application never re-reads
// output), so a link that could attach one would only invite it back.
package terminalsurface

import "fmt"

// Links carries one request to one sidecar unit and answers its reply.
type Links struct {
	Start func(unit string) error
	Send  func(unit string, command string, request map[string]any) (map[string]any, error)
}

func (links Links) start(unit string) error {
	if links.Start == nil {
		return fmt.Errorf("%s has no sidecar starter; the host injected none", unit)
	}
	return links.Start(unit)
}

func (links Links) send(unit, command string, request map[string]any) (map[string]any, error) {
	if links.Send == nil {
		return nil, fmt.Errorf("%s has no sidecar link; the host injected none", command)
	}
	return links.Send(unit, command, request)
}
