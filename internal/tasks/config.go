package tasks

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Destination is a Trello list tasks are read from and added to, with the
// tag its tasks are given.
type Destination struct {
	Board, List, Tag string
}

// String names d as "board / list".
func (d Destination) String() string { return d.Board + " / " + d.List }

// Config is what the dashboard takes from the task program's configuration:
// the Trello credentials, the lists its tasks come from, and the tags whose
// tasks the dashboard leaves out.
type Config struct {
	APIKey, Token, BaseURL string

	// Lists are the Trello lists tasks come from, by board then list.
	Lists []Destination

	IgnoreTags []string
}

// configFiles are the task program's configuration files, in the home
// directory: settings in the second, which holds the secrets, win.
var configFiles = []string{".task.yaml", ".task.secret.yaml"}

// LoadConfig reads the task program's configuration from the home
// directory. Missing files are not an error; they leave nothing configured.
func LoadConfig() (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, fmt.Errorf("finding the task configuration: %w", err)
	}
	var files [][]byte
	for _, name := range configFiles {
		path := filepath.Join(home, name)
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return Config{}, fmt.Errorf("reading the task configuration: %w", err)
		}
		if _, err := parseConfig(data); err != nil {
			return Config{}, fmt.Errorf("parsing %s: %w", path, err)
		}
		files = append(files, data)
	}
	return parseConfig(files...)
}

// configFile is the part of a configuration file the dashboard reads. A
// setting absent from a file is nil, so that a later file overrides only
// what it names; anything else in the file is ignored.
type configFile struct {
	IgnoreTags []string `yaml:"ignore-tags"`
	Trello     *struct {
		APIKey  *string                      `yaml:"api-key"`
		Token   *string                      `yaml:"token"`
		BaseURL *string                      `yaml:"base-url"`
		Tasks   map[string]map[string]string `yaml:"tasks"`
	} `yaml:"trello"`
}

// parseConfig reads the configuration from the files' contents, each
// overriding what the ones before it set.
func parseConfig(files ...[]byte) (Config, error) {
	var c Config
	var lists map[string]map[string]string
	for _, data := range files {
		var f configFile
		if err := yaml.Unmarshal(data, &f); err != nil {
			return Config{}, err
		}
		if f.IgnoreTags != nil {
			c.IgnoreTags = f.IgnoreTags
		}
		if t := f.Trello; t != nil {
			if t.APIKey != nil {
				c.APIKey = *t.APIKey
			}
			if t.Token != nil {
				c.Token = *t.Token
			}
			if t.BaseURL != nil {
				c.BaseURL = *t.BaseURL
			}
			if t.Tasks != nil {
				lists = t.Tasks
			}
		}
	}
	for board, byList := range lists {
		for list, tag := range byList {
			c.Lists = append(c.Lists, Destination{Board: board, List: list, Tag: tag})
		}
	}
	slices.SortFunc(c.Lists, func(x, y Destination) int {
		if n := strings.Compare(x.Board, y.Board); n != 0 {
			return n
		}
		return strings.Compare(x.List, y.List)
	})
	return c, nil
}
