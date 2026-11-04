package cli

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/lesismal/fit/adapter"

	"github.com/lesismal/fit/admin"
	"github.com/lesismal/fit/admin/store"
	"github.com/lesismal/fit/config"
	fitruntime "github.com/lesismal/fit/runtime"
)

// storeBoot is a config kept in a store: the store, the bootstrap file's
// admin, as JSON, and the base config, without admin, that fit runs from.
type storeBoot struct {
	st    *store.Store
	admin []byte
	base  []byte
	// seeded is a store that did not exist, whose base config is the
	// config files', not written yet; merged is one that did, whose
	// config was merged onto the files', and differs from what it held.
	seeded, merged bool
	// stored is the config the store held, before the merge; users are
	// the console users it keeps beside the config.
	stored []byte
	users  []store.User
}

// full returns the config fit runs: the base config with admin.
func (b *storeBoot) full() []byte {
	if b.admin == nil {
		return b.base
	}
	return admin.WithAdmin(b.base, b.admin)
}

// storeSpec says where the store is when admin.store does not say it: a
// .store file given with -c, which is used, or the store beside the config
// files, used when it exists or create is set (fit run -admin).
type storeSpec struct {
	path     string
	explicit bool
	create   bool
}

// StorePasswordEnv is the environment variable holding the password of a
// store admin.store does not describe; without it, the password is the key
// file beside the store, the store's path with ".key" added.
const StorePasswordEnv = "FIT_STORE_PASSWORD"

// openStore opens the store of the config data, standard JSON: the one
// admin.store names, at spec's path when spec is explicit, or else the one
// spec describes. It returns nil when there is none. When the store holds a
// config, the config's members beside admin are its seed, and what the
// store holds is merged onto them, the store's winning.
func openStore(data []byte, spec storeSpec) (*storeBoot, error) {
	adminJSON, rest, err := admin.SplitAdmin(data)
	if err != nil {
		return nil, nil // loading the config reports what is wrong
	}
	path, password, err := resolveStore(adminJSON, spec)
	if path == "" || err != nil {
		return nil, err
	}
	st, base, err := store.Open(path, password)
	if err != nil {
		return nil, err
	}
	users := st.Users()
	// The store keeps the password hashes of the console users changed
	// since the bootstrap file was written; they replace its own, so that
	// a restart logs in with the changed passwords.
	b := &storeBoot{st: st, base: base, stored: base, users: users}
	if adminJSON != nil {
		b.admin = admin.WithConsoleUsers(adminJSON, users)
	}
	if base == nil {
		b.base, b.seeded = rest, true
		return b, nil
	}
	merged, err := admin.MergeConfig(rest, base)
	if err != nil {
		return nil, fmt.Errorf("merging the store %s onto the config files: %w", path, err)
	}
	if !jsonEqual(merged, base) {
		b.base, b.merged = merged, true
	}
	return b, nil
}

// resolveStore returns the path and the password of the store that goes
// with adminJSON, the config's admin (nil when it has none) and spec, or ""
// when there is none.
func resolveStore(adminJSON []byte, spec storeSpec) (path, password string, err error) {
	var a struct {
		Store *config.Store `json:"store"`
	}
	if adminJSON != nil {
		_ = json.Unmarshal(adminJSON, &a)
	}
	switch {
	case a.Store != nil && a.Store.Path != "":
		path = a.Store.Path
		if spec.explicit {
			path = spec.path
		}
		password, err = storePassword(a.Store)
	case spec.path != "" && (spec.explicit || spec.create || fileExists(spec.path)):
		path = spec.path
		password, err = implicitStorePassword(path)
	default:
		return "", "", nil
	}
	return path, password, err
}

// implicitStorePassword returns the password of the store at path, which
// admin.store does not describe: $FIT_STORE_PASSWORD, or the key file beside
// it, which is made, 0600, with a random key when neither it nor the store
// exists yet.
func implicitStorePassword(path string) (string, error) {
	if p := os.Getenv(StorePasswordEnv); p != "" {
		return p, nil
	}
	keyPath := path + ".key"
	data, err := os.ReadFile(keyPath)
	if err == nil {
		p := strings.TrimSpace(string(data))
		if p == "" {
			return "", fmt.Errorf("the store's key file %s is empty", keyPath)
		}
		return p, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("the store's key file: %w", err)
	}
	if fileExists(path) {
		return "", fmt.Errorf("the store %s has no key file %s beside it: copy it there, or set $%s to the store's password", path, keyPath, StorePasswordEnv)
	}
	key := rand.Text() + rand.Text()
	f, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("the store's key file: %w", err)
	}
	_, err = f.WriteString(key + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(keyPath)
		return "", fmt.Errorf("the store's key file: %w", err)
	}
	return key, nil
}

func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

// jsonEqual reports whether a and b are the same JSON value.
func jsonEqual(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// splitStorePaths returns the config paths without the .store file among
// them, and that file, "" when there is none; more than one is an error.
func splitStorePaths(paths []string) (files []string, storeFile string, err error) {
	for _, p := range paths {
		if strings.EqualFold(filepath.Ext(p), ".store") {
			if storeFile != "" {
				return nil, "", fmt.Errorf("-c names two stores, %s and %s; give one", storeFile, p)
			}
			storeFile = p
			continue
		}
		files = append(files, p)
	}
	return files, storeFile, nil
}

// defaultStorePath is the store kept beside the config files when -c names
// none and admin.store says nothing: the first's path with .store for its
// extension (a directory's with .store added), or fit.store with no file.
func defaultStorePath(files []string) string {
	if len(files) == 0 || files[0] == "-" {
		return "fit.store"
	}
	p := filepath.Clean(files[0])
	if fi, err := os.Stat(p); err == nil && fi.IsDir() {
		return p + ".store"
	}
	return strings.TrimSuffix(p, filepath.Ext(p)) + ".store"
}

// loadBoot reads the config files paths name, a .store among them aside,
// and says which store goes with them. given is whether -c was given at
// all: without it, fit.json, when there is no fit.json but there is a
// fit.store, or create is set, fit runs from the store alone.
func loadBoot(paths []string, given, create bool) (*adapter.Set, []adapter.Source, storeSpec, error) {
	files, storeFile, err := splitStorePaths(paths)
	if err != nil {
		return nil, nil, storeSpec{}, err
	}
	if !given && !fileExists(files[0]) && (create || fileExists(defaultStorePath(nil))) {
		files = nil
	}
	spec := storeSpec{path: storeFile, explicit: storeFile != "", create: create}
	if storeFile == "" {
		spec.path = defaultStorePath(files)
	}
	if len(files) == 0 {
		set, err := adapter.AdaptSet(nil)
		return set, nil, spec, err
	}
	set, srcs, err := loadSet(files)
	return set, srcs, spec, err
}

// storePassword returns the password of the store s describes, from where
// it says.
func storePassword(s *config.Store) (string, error) {
	switch {
	case s.Password != "" && s.PasswordFile == "" && s.PasswordEnv == "":
		return s.Password, nil
	case s.PasswordFile != "" && s.Password == "" && s.PasswordEnv == "":
		data, err := os.ReadFile(s.PasswordFile)
		if err != nil {
			return "", fmt.Errorf("admin.store.password_file: %w", err)
		}
		p := strings.TrimSpace(string(data))
		if p == "" {
			return "", fmt.Errorf("admin.store.password_file %s is empty", s.PasswordFile)
		}
		return p, nil
	case s.PasswordEnv != "" && s.Password == "" && s.PasswordFile == "":
		p := os.Getenv(s.PasswordEnv)
		if p == "" {
			return "", fmt.Errorf("admin.store.password_env: $%s is empty or not set", s.PasswordEnv)
		}
		return p, nil
	}
	return "", errors.New("admin.store: set exactly one of password, password_file and password_env")
}

const storeUsage = `Usage:

	fit store export -c <config file or .store>
	        print the config the store holds, as JSON
	fit store import -c <config file or .store> <config file or directory>...
	        check the config the files hold and seal it as the store,
	        replacing it, with a new salt

The config file is the one fit run is started with: its admin.store says
where the store is and its password. Without admin.store, the store is the
.store file -c names, or the one beside the config file (fit.json gives
fit.store), and its password is $FIT_STORE_PASSWORD or the key file beside
it, the store's path with .key added. To change the password, export the
store, change the password, and import what was exported.
`

func runStore(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "export" && args[0] != "import" {
		fmt.Fprint(stderr, storeUsage)
		return 2
	}
	cmd := "fit store " + args[0]
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	var cfgPaths configPaths
	flags.Var(&cfgPaths, "c", "the config file fit run is started with, whose admin.store names the store, or the .store file itself")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if args[0] == "export" && flags.NArg() > 0 || args[0] == "import" && flags.NArg() == 0 {
		fmt.Fprint(stderr, storeUsage)
		return 2
	}
	paths := cfgPaths.orDefault()
	set, _, spec, err := loadBoot(paths, len(cfgPaths) > 0, false)
	if err != nil {
		printConfigError(stderr, cmd, strings.Join(paths, ", "), err)
		return 1
	}
	// An import makes the store when there is none yet.
	spec.create = args[0] == "import"
	std, err := config.Standardize(set.JSON)
	if err != nil {
		printConfigError(stderr, cmd, strings.Join(paths, ", "), err)
		return 1
	}
	adminJSON, _, _ := admin.SplitAdmin(std)
	if args[0] == "export" {
		b, err := openStore(std, spec)
		if b == nil && err == nil {
			err = fmt.Errorf("%s names no store: no admin.store, and no %s", strings.Join(paths, ", "), spec.path)
		}
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", cmd, err)
			return 1
		}
		if b.seeded {
			fmt.Fprintf(stderr, "%s: there is no store at %s yet\n", cmd, b.st.Path())
			return 1
		}
		_, _ = stdout.Write(b.stored)
		return 0
	}
	path, password, err := resolveStore(adminJSON, spec)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", cmd, err)
		return 1
	}
	// An import replaces the config, not the console's passwords: the users
	// the old store keeps stay, when it opens; one that does not, as after
	// the password was changed, is no reason to stop.
	var users []store.User
	if old, _, err := store.Open(path, password); err == nil {
		users = old.Users()
	} else if !errors.Is(err, store.ErrOpen) {
		fmt.Fprintf(stderr, "%s: %v\n", cmd, err)
		return 1
	}
	st, err := store.Create(path, password)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", cmd, err)
		return 1
	}

	name := strings.Join(flags.Args(), ", ")
	in, _, err := loadSet(flags.Args())
	var data []byte
	if err == nil {
		data, err = config.Standardize(in.JSON)
	}
	if err == nil {
		if inAdmin, _, _ := admin.SplitAdmin(data); inAdmin != nil {
			err = config.Errors{{Path: "admin", Msg: "admin stays in the bootstrap file: leave it out of what is imported"}}
		}
	}
	if err == nil {
		full := data
		if adminJSON != nil {
			full = admin.WithAdmin(data, adminJSON)
		}
		var c *config.Config
		if c, err = config.Load(full); err == nil {
			var g *fitruntime.Graph
			if g, err = fitruntime.Compile(c, fitruntime.Env{}); err == nil {
				g.Release()
			}
		}
		err = in.MapErrors(err)
	}
	if err != nil {
		printConfigError(stderr, cmd, name, err)
		return 1
	}
	if err := st.Write(data); err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", cmd, err)
		return 1
	}
	if len(users) > 0 {
		if err := st.SetUsers(users); err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", cmd, err)
			return 1
		}
	}
	fmt.Fprintf(stdout, "%s: sealed into %s\n", name, st.Path())
	return 0
}
