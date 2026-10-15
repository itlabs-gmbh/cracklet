package container

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/itlabs-gmbh/cracklet/internal/config"
	"github.com/itlabs-gmbh/cracklet/internal/sshcfg"
)

// keyscanTimeout is ssh-keyscan's per-connection timeout in seconds; the VM
// has just answered on port 22, so a long wait only hides a problem.
const keyscanTimeout = "5"

// errReplaced means the name was given to another VM while this one's key
// was read; the record and the machine under that name are the other VM's.
var errReplaced = errors.New("replaced by another VM of the same name while its host key was read")

// pinHostKeys reads the host key of a VM that has just booted for the first
// time and stores it with the record, then rewrites known_hosts. Every guest
// shares one vmnet /24 and nothing keeps a guest from taking a neighbour's
// address, so from here on ssh refuses any other key under this name. The
// key is read before any session (grant, broker) reaches the VM; a neighbour
// would have to win a race against a machine that booted half a second ago.
func (b *Backend) pinHostKeys(ctx context.Context, name, id, ip string) error {
	out, err := b.r.Output(ctx, "ssh-keyscan", "-q", "-t", "ed25519", "-T", keyscanTimeout, ip)
	if err != nil {
		return fmt.Errorf("read the host key of %s: %w", name, err)
	}
	keys := parseHostKeys(string(out), ip)
	if len(keys) == 0 {
		return fmt.Errorf("%s answered on ssh but presented no host key (ssh-keyscan %s)", name, ip)
	}
	release, err := b.lock(ctx)
	if err != nil {
		return err
	}
	defer release()
	rec, ok, err := b.loadRecord(name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s was removed while it booted", name)
	}
	if rec.ID != id {
		return fmt.Errorf("%s was %w", name, errReplaced)
	}
	rec.HostKeys = keys
	if err := b.saveRecord(name, rec); err != nil {
		return err
	}
	return b.writeKnownHosts()
}

// parseHostKeys keeps the "TYPE KEY" part of every ssh-keyscan line for addr.
func parseHostKeys(out, addr string) []string {
	var keys []string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == addr {
			keys = append(keys, fields[1]+" "+fields[2])
		}
	}
	return keys
}

// writeKnownHosts renders the pinned key of every recorded VM under its ssh
// alias. It is regenerated from the records whenever one changes (under the
// lock), so a VM recreated under an old name never trips over its
// predecessor's key.
func (b *Backend) writeKnownHosts() error {
	names, err := b.recordNames()
	if err != nil {
		return err
	}
	var lines strings.Builder
	for _, name := range names {
		rec, _, err := b.loadRecord(name)
		if errors.Is(err, errCorruptRecord) {
			continue
		}
		if err != nil {
			return err
		}
		for _, key := range rec.HostKeys {
			lines.WriteString(sshcfg.HostAlias(name, config.Instance) + " " + key + "\n")
		}
	}
	if err := os.MkdirAll(b.paths.Home, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", b.paths.Home, err)
	}
	return writeAtomic(b.paths.KnownHostsPath(), []byte(lines.String()))
}
