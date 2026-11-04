package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

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
	// bootstrap file's, not written yet; ignored are the bootstrap file's
	// members other than admin left out because the store exists.
	seeded  bool
	ignored []string
}

// full returns the config fit runs: the base config with admin.
func (b *storeBoot) full() []byte { return admin.WithAdmin(b.base, b.admin) }

// openStore opens the store the bootstrap config data, standard JSON, names
// in admin.store, or returns nil when it names none.
func openStore(data []byte) (*storeBoot, error) {
	adminJSON, rest, err := admin.SplitAdmin(data)
	if err != nil || adminJSON == nil {
		return nil, nil // loading the config reports what is wrong
	}
	var a struct {
		Store *config.Store `json:"store"`
	}
	if json.Unmarshal(adminJSON, &a) != nil || a.Store == nil || a.Store.Path == "" {
		return nil, nil
	}
	password, err := storePassword(a.Store)
	if err != nil {
		return nil, err
	}
	st, base, err := store.Open(a.Store.Path, password)
	if err != nil {
		return nil, err
	}
	// The store keeps the password hashes of the console users changed
	// since the bootstrap file was written; they replace its own, so that
	// a restart logs in with the changed passwords.
	b := &storeBoot{st: st, admin: admin.WithConsoleUsers(adminJSON, st.Users()), base: base}
	if base == nil {
		b.base, b.seeded = rest, true
		return b, nil
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(rest, &top) == nil {
		for k := range top {
			if k != "schema_version" {
				b.ignored = append(b.ignored, k)
			}
		}
	}
	return b, nil
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

	fit store export -c <bootstrap file>
	        print the config the store holds, as JSON
	fit store import -c <bootstrap file> <config file or directory>...
	        check the config the files hold and seal it as the store,
	        replacing it, with a new salt

The bootstrap file is the one fit run is started with: its admin.store says
where the store is and its password. To change the password, export the
store, change the bootstrap file, and import what was exported.
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
	flags.Var(&cfgPaths, "c", "the bootstrap file, whose admin.store names the store")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if args[0] == "export" && flags.NArg() > 0 || args[0] == "import" && flags.NArg() == 0 {
		fmt.Fprint(stderr, storeUsage)
		return 2
	}
	paths := cfgPaths.orDefault()
	set, _, err := loadSet(paths)
	if err != nil {
		printConfigError(stderr, cmd, strings.Join(paths, ", "), err)
		return 1
	}
	b, err := openStore(set.JSON)
	if b == nil && err == nil {
		err = fmt.Errorf("%s sets no admin.store", strings.Join(paths, ", "))
	}
	if args[0] == "export" {
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", cmd, err)
			return 1
		}
		if b.seeded {
			fmt.Fprintf(stderr, "%s: there is no store at %s yet\n", cmd, b.st.Path())
			return 1
		}
		_, _ = stdout.Write(b.base)
		return 0
	}
	// An import replaces the store, so one that does not open, as after
	// the password was changed, is no reason to stop.
	if err != nil && !errors.Is(err, store.ErrOpen) {
		fmt.Fprintf(stderr, "%s: %v\n", cmd, err)
		return 1
	}
	adminJSON, _, _ := admin.SplitAdmin(set.JSON)
	var a struct {
		Store *config.Store `json:"store"`
	}
	_ = json.Unmarshal(adminJSON, &a) // openStore read it
	password, _ := storePassword(a.Store)
	// An import replaces the config, not the console's passwords: the users
	// the old store keeps stay, when it opens.
	var users []store.User
	if b != nil {
		users = b.st.Users()
	}
	st, err := store.Create(a.Store.Path, password)
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
		var c *config.Config
		if c, err = config.Load(admin.WithAdmin(data, adminJSON)); err == nil {
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
