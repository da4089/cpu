// Copyright 2018-2019 the u-root Authors. All rights reserved
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package client

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	// We use this ssh because it implements port redirection.
	// It can not, however, unpack password-protected keys yet.

	config "github.com/kevinburke/ssh_config"

	// We use this ssh because it can unpack password-protected private keys.
	ssh "golang.org/x/crypto/ssh"
)

const (
	// DefaultPort is the default cpu port.
	DefaultPort = "17010"
)

var (
	// DefaultKeyFile is the default key for cpu users.
	DefaultKeyFile = filepath.Join(os.Getenv("HOME"), ".ssh/cpu_rsa")
	// Debug9p enables 9p debugging.
	Debug9p bool
	// Dump9p enables dumping 9p packets.
	Dump9p bool
	// DumpWriter is an io.Writer to which dump packets are written.
	DumpWriter io.Writer = os.Stderr
)

// a nonce is a [32]byte containing only printable characters, suitable for use as a string
type nonce [32]byte

func verbose(f string, a ...interface{}) {
	V("\r\n"+f+"\r\n", a...)
}

// generateNonce returns a nonce, or an error if random reader fails.
func generateNonce() (nonce, error) {
	var b [len(nonce{}) / 2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nonce{}, err
	}
	var n nonce
	copy(n[:], fmt.Sprintf("%02x", b))
	return n, nil
}

// String is a Stringer for nonce.
func (n nonce) String() string {
	return string(n[:])
}

// UserKeyConfig sets up authentication for a User Key.
// It is required in almost all cases.
func (c *Cmd) UserKeyConfig() error {
	kf := c.PrivateKeyFile
	if len(kf) == 0 {
		kf = config.Get(c.Host, "IdentityFile")
		V("key file from config is %q", kf)
		if len(kf) == 0 {
			kf = DefaultKeyFile
		}
	}
	// The kf will always be non-zero at this point.
	if strings.HasPrefix(kf, "~/") {
		kf = filepath.Join(os.Getenv("HOME"), kf[1:])
	}
	key, err := ioutil.ReadFile(kf)
	if err != nil {
		return fmt.Errorf("unable to read private key %q: %v", kf, err)
	}

	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return fmt.Errorf("ParsePrivateKey %q: %v", kf, err)
	}
	c.config.Auth = append(c.config.Auth, ssh.PublicKeys(signer))
	return nil
}

// HostKeyConfig sets the host key. It is optional.
func (c *Cmd) HostKeyConfig(hostKeyFile string) error {
	hk, err := ioutil.ReadFile(hostKeyFile)
	if err != nil {
		return fmt.Errorf("unable to read host key %v: %v", hostKeyFile, err)
	}
	pk, err := ssh.ParsePublicKey(hk)
	if err != nil {
		return fmt.Errorf("host key %v: %v", string(hk), err)
	}
	c.config.HostKeyCallback = ssh.FixedHostKey(pk)
	return nil
}

// SetEnv sets zero or more environment variables for a Session.
func (c *Cmd) SetEnv(envs ...string) error {
	for _, v := range append(os.Environ(), envs...) {
		env := strings.SplitN(v, "=", 2)
		if len(env) == 1 {
			env = append(env, "")
		}
		if err := c.session.Setenv(env[0], env[1]); err != nil {
			return fmt.Errorf("Warning: c.session.Setenv(%q, %q): %v", v, os.Getenv(v), err)
		}
	}
	return nil
}

// SSHStdin implements an ssh-like reader, honoring ~ commands.
func (c *Cmd) SSHStdin(w io.WriteCloser, r io.Reader) {
	var newLine, tilde bool
	var t = []byte{'~'}
	var b [1]byte
	for {
		if _, err := r.Read(b[:]); err != nil {
			break
		}
		switch b[0] {
		default:
			newLine = false
			if tilde {
				if _, err := w.Write(t[:]); err != nil {
					return
				}
				tilde = false
			}
			if _, err := w.Write(b[:]); err != nil {
				return
			}
		case '\n', '\r':
			newLine = true
			if _, err := w.Write(b[:]); err != nil {
				return
			}
		case '~':
			if newLine {
				newLine = false
				tilde = true
				break
			}
			if _, err := w.Write(t[:]); err != nil {
				return
			}
		case '.':
			if tilde {
				c.session.Close()
				return
			}
			if _, err := w.Write(b[:]); err != nil {
				return
			}
		}
	}
}

// GetKeyFile picks a keyfile if none has been set.
// It will use ssh config, else use a default.
func GetKeyFile(host, kf string) string {
	V("getKeyFile for %q", kf)
	if len(kf) == 0 {
		kf = config.Get(host, "IdentityFile")
		V("key file from config is %q", kf)
		if len(kf) == 0 {
			kf = DefaultKeyFile
		}
	}
	// The kf will always be non-zero at this point.
	if strings.HasPrefix(kf, "~") {
		kf = filepath.Join(os.Getenv("HOME"), kf[1:])
	}
	V("getKeyFile returns %q", kf)
	// this is a tad annoying, but the config package doesn't handle ~.
	return kf
}

// GetHostName reads the host name from the ssh config file,
// if needed. If it is not found, the host name is returned.
func GetHostName(host string) string {
	h := config.Get(host, "HostName")
	if len(h) != 0 {
		host = h
	}
	return host
}

// GetPort gets a port. It verifies that the port fits in 16-bit space.
// The rules here are messy, since config.Get will return "22" if
// there is no entry in .ssh/config. 22 is not allowed. So in the case
// of "22", convert to defaultPort.
func GetPort(host, port string) (string, error) {
	p := port
	V("getPort(%q, %q)", host, port)
	if len(port) == 0 {
		if cp := config.Get(host, "Port"); len(cp) != 0 {
			V("config.Get(%q,%q): %q", host, port, cp)
			p = cp
		}
	}
	if len(p) == 0 || p == "22" {
		p = DefaultPort
		V("getPort: return default %q", p)
	}
	V("returns %q", p)
	return p, nil
}

// vsockIdPort gets a client id and a port from host and port
// The id and port are uint32.
func vsockIdPort(host, port string) (uint32, uint32, error) {
	h, err := strconv.ParseUint(host, 0, 32)
	if err != nil {
		return 0, 0, err
	}
	p, err := strconv.ParseUint(port, 0, 32)
	if err != nil {
		return 0, 0, err
	}
	return uint32(h), uint32(p), nil
}

// Signal implements ssh.Signal
func (c *Cmd) Signal(s ssh.Signal) error {
	return c.session.Signal(s)
}

// Outputs returns a slice of bytes.Buffer for stdout and stderr,
// and an error if either had trouble being read.
func (c *Cmd) Outputs() ([]bytes.Buffer, error) {
	var r [2]bytes.Buffer
	var errs []error
	if _, err := io.Copy(&r[0], c.Stdout); err != nil && err != io.EOF {
		errs = append(errs, fmt.Errorf("Stdout: %w", err))
	}
	if _, err := io.Copy(&r[1], c.Stderr); err != nil && err != io.EOF {
		errs = append(errs, fmt.Errorf("Stderr: %w", err))
	}
	if errs != nil {
		return r[:], fmt.Errorf(fmt.Sprintf("%v", errs))
	}
	return r[:], nil
}

// parseBinds parses a CPU_NAMESPACE-style string to a
// an fstab format string. It is not intended to be called
// outside this package, because it is sensitive to values
// in the Cmd struct (e.g. c.TmpMnt) and users might set things
// in the wrong order. E.g., were we to call this function before
// before code called WithTmpMnt, the binds will be to the wrong
// place. This might be fixed with more complex cpud
// behavior, e.g. pass fstab as a template and let cpud change it
// before use, but for now, we will see if we can get away
// with limiting the mistakes a client can make.
func parseBinds(s, tmp string) (string, error) {
	var fstab string
	if len(s) == 0 {
		return fstab, nil
	}
	binds := strings.Split(s, ":")
	for i, bind := range binds {
		if len(bind) == 0 {
			return "", fmt.Errorf("bind: element %d is zero length:%w", i, strconv.ErrSyntax)
		}
		// If the value is local=remote, len(c) will be 2.
		// The value might be some weird degenerate form such as
		// =name or name=. Both are considered to be an error.
		// The convention is to split on the first =. It is not up
		// to this code to determine that more than one = is an error
		// There is no rule that filenames can not contain an '='!
		c := strings.SplitN(bind, "=", 2)
		var local, remote string
		switch len(c) {
		case 0:
			return fstab, fmt.Errorf("bind: element %d(%q): empty elements are not supported:%w", i, bind, strconv.ErrSyntax)
		case 1:
			local, remote = c[0], c[0]
		case 2:
			local, remote = c[0], c[1]
		default:
			return fstab, fmt.Errorf("bind: element %d(%q): too many elements around = sign:%w", i, bind, strconv.ErrSyntax)
		}
		if len(local) == 0 {
			return fstab, fmt.Errorf("bind: element %d(%q): local is 0 length:%w", i, bind, strconv.ErrSyntax)
		}
		if len(remote) == 0 {
			return fstab, fmt.Errorf("bind: element %d(%q): remote is 0 length:%w", i, bind, strconv.ErrSyntax)
		}

		// The convention is that the remote side is relative to filepath.Join(c.TmpMnt, "cpu")
		// and the left side is taken exactly as written. Further, recall that in bind mounts, the
		// remote side is the "device", and the local side is the "target."
		fstab = fstab + fmt.Sprintf("%s %s none defaults,bind 0 0\n", filepath.Join(tmp, "cpu", remote), local)
	}
	return fstab, nil
}

// joinFSTab joins an arbitrary number of fstab-style strings.
// The intent is to deal with strings that may not be well formatted
// as provided by users, e.g. too many newlines, not enough, and so on.
func joinFSTab(tables ...string) string {
	if len(tables) == 0 {
		return ""
	}
	for i := range tables {
		if len(tables[i]) == 0 {
			continue
		}
		tables[i] = strings.TrimRight(tables[i], "\n")
	}
	return strings.Join(tables, "\n") + "\n"
}
