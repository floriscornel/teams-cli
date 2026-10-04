package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/floriscornel/teams-cli/internal/output"
	"github.com/floriscornel/teams-cli/internal/store"
)

// newCacheCmd implements `teams cache info|clear`. Everything here is local: no
// token is ever read, written or deleted, and PLAN.md's rule that `cache clear`
// must never touch secrets is enforced in deletionTargets.
func (a *App) newCacheCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Inspect and clear the local cache",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(a.newCacheInfoCmd(), a.newCacheClearCmd())
	return cmd
}

type cacheEntry struct {
	Kind    string `json:"kind"`
	Label   string `json:"label"`
	Path    string `json:"path"`
	Dir     bool   `json:"dir"`
	Exists  bool   `json:"exists"`
	Files   int    `json:"files"`
	Bytes   int64  `json:"bytes"`
	Newest  string `json:"newest,omitempty"`
	Age     string `json:"age,omitempty"`
	Problem string `json:"problem,omitempty"`
}

func (a *App) newCacheInfoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info",
		Short: "List every local path with its size, entry count and age",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			eff, err := a.Effective()
			if err != nil {
				return err
			}
			paths, err := a.Paths()
			if err != nil {
				return err
			}
			now := a.Clock.Now()
			// The entity cache is the one rebuildable file whose entry count is worth
			// reporting: it is what name resolution reads and writes (PLAN.md:260).
			entityCache, cacheErr := store.LoadEntities(paths.EntityCacheFile())
			if cacheErr != nil {
				return output.Errorf("%v", cacheErr)
			}
			if problem := entityCache.Problem(); problem != nil {
				a.Printer.Warnf("entity cache: %v; it will be rebuilt on the next lookup", problem)
			}
			cacheStats := entityCache.Stats(now)
			entries := make([]cacheEntry, 0, len(paths.Locations()))
			for _, loc := range paths.Locations() {
				st := store.Scan(loc.Path, loc.Dir)
				entry := cacheEntry{
					Kind:    loc.Kind,
					Label:   loc.Label,
					Path:    loc.Path,
					Dir:     loc.Dir,
					Exists:  st.Exists,
					Files:   st.Files,
					Bytes:   st.Bytes,
					Problem: st.Problem,
				}
				if st.Exists && !st.Newest.IsZero() {
					entry.Newest = st.Newest.UTC().Format(time.RFC3339)
					entry.Age = output.HumanAge(now, st.Newest)
				}
				entries = append(entries, entry)
			}
			if a.Printer.JSONMode() {
				return a.Printer.JSON(map[string]any{
					"profile": eff.Name,
					"paths": map[string]string{
						"config":      paths.ConfigFile,
						"config_dir":  paths.ConfigDir,
						"cache_dir":   paths.CacheDir,
						"state_dir":   paths.StateDir,
						"cache_root":  paths.CacheRoot,
						"state_root":  paths.StateRoot,
						"token_file":  paths.TokenFile(),
						"metadata":    paths.AuthMetadataFile(),
						"entity_file": paths.EntityCacheFile(),
					},
					"entity_entries": cacheStats.Entries,
					"entries":        entries,
				})
			}
			a.Printer.Definitions([][2]string{
				{"profile", eff.Name},
				{"config", paths.ConfigFile},
				{"cache dir", paths.CacheDir},
				{"state dir", paths.StateDir},
			})
			a.Printer.Println()
			rows := make([][]string, 0, len(entries))
			for _, e := range entries {
				size := "-"
				count := "-"
				if e.Exists {
					size = store.HumanBytes(e.Bytes)
					if e.Dir {
						count = fmt.Sprintf("%d files", e.Files)
					} else {
						count = "1 file"
					}
				}
				age := e.Age
				if age == "" {
					age = "-"
				}
				rows = append(rows, []string{e.Kind, e.Label, count, size, age, e.Path})
			}
			a.Printer.Table([]string{"KIND", "WHAT", "ENTRIES", "SIZE", "AGE", "PATH"}, rows)
			// The entity cache is the one entry with a TTL, so its age is called
			// out explicitly (PLAN.md: "plus the age of the entity cache").
			switch {
			case cacheStats.Entries == 0:
				a.Printer.Statusf("entity cache: empty")
			case cacheStats.Oldest.IsZero():
				a.Printer.Statusf("entity cache: %d entries", cacheStats.Entries)
			default:
				a.Printer.Statusf("entity cache: %d entries, oldest %s", cacheStats.Entries, output.HumanAge(now, cacheStats.Oldest))
			}
			return nil
		},
	}
}

type clearPlan struct {
	// Targets are files and directories to delete.
	Targets []string
	// State reports whether the plan deletes user state, which needs
	// confirmation (PLAN.md: on a TTY, ask; non-interactively, require --yes).
	State bool
	// Protected are the paths the plan deliberately keeps, for the summary.
	Protected []string
}

// secretNames are the files `cache clear` must never delete. `auth logout` is
// the command that forgets tokens (PLAN.md "Local data").
var secretNames = map[string]bool{
	filepath.Base("token.bin"):  true,
	filepath.Base("token.json"): true,
	filepath.Base("auth.json"):  true,
}

func isSecretFile(path string) bool {
	base := filepath.Base(path)
	if secretNames[base] {
		return true
	}
	if strings.HasPrefix(base, "msal-cache") {
		return true
	}
	return strings.HasSuffix(base, ".lockfile")
}

// deletionTargets decides what a `cache clear` invocation removes:
//
//	(default)  the rebuildable cache directory
//	--names    only the entity cache file
//	--ai       the rebuildable cache plus AI history and memory
//	--all      everything except the config file and the secrets
//
// It never returns a path under the config directory, and never a secret file.
func deletionTargets(paths store.Paths, names, ai, all bool) (clearPlan, error) {
	plan := clearPlan{}
	add := func(path string, state bool) {
		if path == "" || isSecretFile(path) {
			return
		}
		plan.Targets = append(plan.Targets, path)
		if state {
			plan.State = true
		}
	}

	switch {
	case all:
		add(paths.CacheDir, false)
		entries, err := os.ReadDir(paths.StateDir)
		if err != nil && !os.IsNotExist(err) {
			return plan, output.Errorf("read %s: %v", paths.StateDir, err)
		}
		for _, entry := range entries {
			path := filepath.Join(paths.StateDir, entry.Name())
			if isSecretFile(path) || strings.HasSuffix(entry.Name(), ".lock") {
				plan.Protected = append(plan.Protected, path)
				continue
			}
			add(path, true)
		}
		sort.Strings(plan.Targets)
	case ai:
		add(paths.CacheDir, false)
		add(paths.AIDir(), true)
	case names:
		add(paths.EntityCacheFile(), false)
	default:
		add(paths.CacheDir, false)
	}
	if len(plan.Targets) == 0 {
		return plan, nil
	}
	return plan, nil
}

func (a *App) newCacheClearCmd() *cobra.Command {
	var (
		names bool
		ai    bool
		all   bool
		yes   bool
	)
	cmd := &cobra.Command{
		Use:   "clear",
		Short: "Delete the rebuildable cache (never tokens or config)",
		Long: "Delete local, rebuildable data.\n\n" +
			"With no flags it removes the entity cache; --names removes only the\n" +
			"name/person cache; --ai also removes AI session history and memory;\n" +
			"--all removes everything except the config file and the token secrets.\n\n" +
			"Tokens are never deleted: use `teams auth logout` for that.",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			paths, err := a.Paths()
			if err != nil {
				return err
			}
			plan, err := deletionTargets(paths, names, ai, all)
			if err != nil {
				return err
			}
			if len(plan.Targets) == 0 {
				a.Printer.Successf("nothing to clear")
				return nil
			}
			if plan.State {
				if err := a.confirm(fmt.Sprintf("delete local state at %s?", strings.Join(plan.Targets, ", ")), yes); err != nil {
					return err
				}
			}
			removed := make([]string, 0, len(plan.Targets))
			for _, target := range plan.Targets {
				st := store.Scan(target, isDir(target))
				if !st.Exists {
					continue
				}
				if err := removeTarget(target, st.IsDir); err != nil {
					return err
				}
				removed = append(removed, target)
				a.Printer.Debugf("removed %s", target)
			}
			if a.Printer.JSONMode() {
				return a.Printer.JSON(map[string]any{
					"removed":   removed,
					"protected": plan.Protected,
				})
			}
			if len(removed) == 0 {
				a.Printer.Successf("nothing to clear")
				return nil
			}
			a.Printer.Successf("cleared %d path(s)", len(removed))
			for _, path := range removed {
				a.Printer.Println("  " + path)
			}
			for _, path := range plan.Protected {
				a.Printer.Statusf("kept %s", path)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&names, "names", false, "clear only the name and person cache")
	cmd.Flags().BoolVar(&ai, "ai", false, "also clear AI session history and memory")
	cmd.Flags().BoolVar(&all, "all", false, "clear all local state except config and tokens")
	cmd.Flags().BoolVar(&yes, "yes", false, "do not ask for confirmation")
	return cmd
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

func removeTarget(path string, dir bool) error {
	if dir {
		if err := store.RemoveTree(path); err != nil {
			return output.Errorf("%v", err)
		}
		return nil
	}
	if err := store.RemoveIfExists(path); err != nil {
		return output.Errorf("%v", err)
	}
	return nil
}
