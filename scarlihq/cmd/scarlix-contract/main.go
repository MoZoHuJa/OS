// scarlix-contract — Resource Contract v1 CLI.
//
// Usage:
//
//	scarlix-contract example            — print example contract as YAML
//	scarlix-contract example --json     — print example contract as JSON
//	scarlix-contract validate <file>    — parse + validate a contract file
//	                                     (YAML or JSON by extension)
//	scarlix-contract parse <file>       — parse + print normalized JSON
//
// Exit codes:
//
//	0 — success
//	1 — parse/validation error
//	2 — usage error
//
// The contract schema is FROZEN in v19.1.0. See
// docs/SCARLIX_RESOURCE_CONTRACT.md and the ScaRgeN master guide section 10.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/MoZoHuJa/OS/scarlihq/internal/contract"
)

const usage = `scarlix-contract — Resource Contract v1 CLI (SCARLIX OS v19.1.0)

Usage:
  scarlix-contract example              Print example contract as YAML
  scarlix-contract example --json       Print example contract as JSON
  scarlix-contract validate <file>      Parse + validate a contract file
                                        (YAML or JSON, by extension)
  scarlix-contract parse <file>         Parse + print normalized JSON

Exit codes:
  0  success
  1  parse/validation error
  2  usage error
`

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	cmd := args[0]
	rest := args[1:]

	switch cmd {
	case "example":
		if err := runExample(rest); err != nil {
			fmt.Fprintf(os.Stderr, "scarlix-contract: error: %v\n", err)
			os.Exit(1)
		}
	case "validate":
		if err := runValidate(rest); err != nil {
			fmt.Fprintf(os.Stderr, "scarlix-contract: error: %v\n", err)
			os.Exit(1)
		}
	case "parse":
		if err := runParse(rest); err != nil {
			fmt.Fprintf(os.Stderr, "scarlix-contract: error: %v\n", err)
			os.Exit(1)
		}
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "scarlix-contract: unknown command %q\n\n", cmd)
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

// runExample prints contract.Example() as YAML (default) or JSON (--json).
func runExample(args []string) error {
	asJSON := false
	for _, a := range args {
		switch a {
		case "--json":
			asJSON = true
		case "-h", "--help":
			fmt.Print(usage)
			return nil
		default:
			return fmt.Errorf("unknown flag %q for 'example' command", a)
		}
	}
	c := contract.Example()
	if asJSON {
		out, err := contract.MarshalJSON(c)
		if err != nil {
			return err
		}
		fmt.Println(string(out))
		return nil
	}
	out, err := contract.MarshalYAML(c)
	if err != nil {
		return err
	}
	// yaml.Marshal already appends a trailing newline.
	fmt.Print(string(out))
	return nil
}

// runValidate parses a contract file (YAML or JSON by extension) and
// prints "valid" on success, or the parse error on failure.
func runValidate(args []string) error {
	if len(args) != 1 {
		return errors.New("validate: exactly one file argument required")
	}
	path := args[0]
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if _, err := parseByExtension(path, data); err != nil {
		return fmt.Errorf("validate %s: %w", path, err)
	}
	fmt.Println("valid")
	return nil
}

// runParse parses a contract file and prints normalized JSON to stdout.
func runParse(args []string) error {
	if len(args) != 1 {
		return errors.New("parse: exactly one file argument required")
	}
	path := args[0]
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	c, err := parseByExtension(path, data)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	out, err := contract.MarshalJSON(c)
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
}

// parseByExtension parses a contract from data using the format implied
// by the file extension (.yaml/.yml → YAML, .json → JSON). Unknown
// extensions default to YAML (matches the canonical example.yaml).
func parseByExtension(path string, data []byte) (*contract.ResourceContract, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".json":
		return contract.ParseJSON(data)
	default: // .yaml, .yml, or unknown
		return contract.ParseYAML(data)
	}
}
