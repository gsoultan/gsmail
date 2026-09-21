// Command surface counts the module's exported API and compares it against a
// recorded budget.
//
// The v1 audit in docs/v1-readiness.md measured the surface twice, by hand,
// and the second measurement silently omitted a whole package -- gsmailtest,
// 29 exported symbols, public and documented since v0.8.0. A surface does not
// get too large through one bad decision; it gets there through a run of
// individually defensible ones, and an audit nobody re-runs cannot see that
// happening. So the count lives here instead of in a person's shell history.
//
// What counts, exactly, so the method is never unstated again:
//
//   - exported top-level types, funcs, vars and consts;
//   - exported methods whose receiver type is itself exported -- a method on an
//     unexported type is unreachable, so it is not surface;
//   - exported methods declared in an exported interface, which a caller must
//     implement and v1 would freeze;
//   - excluding internal/, examples/, testdata/, _test.go files and package
//     main, none of which a caller can import.
//
// Usage:
//
//	surface                 # print the count per package
//	surface -v              # also list every symbol
//	surface -check <file>   # exit 1 if the count has drifted from the budget
//
// A drift is not a failure in itself -- it means the budget is now a decision
// somebody has to make on purpose, by editing the file.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const rootPkg = "gsmail (root)"

func main() {
	var (
		verbose = flag.Bool("v", false, "list every exported symbol")
		check   = flag.String("check", "", "compare against a budget file and exit 1 on drift")
		dir     = flag.String("dir", ".", "module root to measure")
	)
	flag.Parse()

	counts, syms, err := measure(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "surface:", err)
		os.Exit(2)
	}

	if *check == "" {
		report(counts, syms, *verbose)
		return
	}

	budget, err := readBudget(*check)
	if err != nil {
		fmt.Fprintln(os.Stderr, "surface:", err)
		os.Exit(2)
	}
	if drift := compare(budget, counts); len(drift) > 0 {
		fmt.Fprintf(os.Stderr, "surface: the exported API no longer matches %s\n\n", *check)
		for _, d := range drift {
			fmt.Fprintln(os.Stderr, "  "+d)
		}
		fmt.Fprintf(os.Stderr, "\nIf the change is intended, edit %s to the new number in the\n", *check)
		fmt.Fprintln(os.Stderr, "same commit. That edit is the point: it is where the size of the")
		fmt.Fprintln(os.Stderr, "surface stops being an accident and becomes a decision.")
		os.Exit(1)
	}
	total := 0
	for _, n := range counts {
		total += n
	}
	fmt.Printf("surface: %d exported symbols across %d packages, matching budget\n", total, len(counts))
}

func measure(root string) (map[string]int, map[string][]string, error) {
	counts := map[string]int{}
	syms := map[string][]string{}

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch b := d.Name(); {
			case b == "internal", b == "examples", b == "testdata", b == ".git", b == ".idea":
				return filepath.SkipDir
			case strings.HasPrefix(b, "graphify"):
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		pkg := filepath.Dir(rel)
		if pkg == "." {
			pkg = rootPkg
		}

		f, err := parser.ParseFile(token.NewFileSet(), p, nil, 0)
		if err != nil {
			return fmt.Errorf("parsing %s: %w", rel, err)
		}
		if f.Name.Name == "main" || strings.HasSuffix(f.Name.Name, "_test") {
			return nil
		}
		for _, s := range exported(f) {
			counts[pkg]++
			syms[pkg] = append(syms[pkg], s)
		}
		return nil
	})
	return counts, syms, err
}

func exported(f *ast.File) []string {
	var out []string
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if !d.Name.IsExported() {
				continue
			}
			if d.Recv == nil || len(d.Recv.List) == 0 {
				out = append(out, d.Name.Name+" [func]")
				continue
			}
			// A method is only reachable if its receiver is exported too.
			if recv := receiverName(d.Recv.List[0].Type); ast.IsExported(recv) {
				out = append(out, recv+"."+d.Name.Name+" [method]")
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if !s.Name.IsExported() {
						continue
					}
					out = append(out, s.Name.Name+" [type]")
					it, ok := s.Type.(*ast.InterfaceType)
					if !ok {
						continue
					}
					for _, m := range it.Methods.List {
						for _, name := range m.Names {
							if name.IsExported() {
								out = append(out, s.Name.Name+"."+name.Name+" [interface method]")
							}
						}
					}
				case *ast.ValueSpec:
					kind := "var"
					if d.Tok == token.CONST {
						kind = "const"
					}
					for _, name := range s.Names {
						if name.IsExported() {
							out = append(out, name.Name+" ["+kind+"]")
						}
					}
				}
			}
		}
	}
	return out
}

func receiverName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return receiverName(t.X)
	case *ast.IndexExpr: // generic receiver, Type[T]
		return receiverName(t.X)
	case *ast.IndexListExpr:
		return receiverName(t.X)
	case *ast.Ident:
		return t.Name
	}
	return ""
}

func readBudget(path string) (map[string]int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	budget := map[string]int{}
	for i, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		at := strings.LastIndexFunc(line, func(r rune) bool { return r == ' ' || r == '\t' })
		if at < 0 {
			return nil, fmt.Errorf("%s:%d: want \"<package> <count>\", got %q", path, i+1, line)
		}
		n, err := strconv.Atoi(strings.TrimSpace(line[at:]))
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, i+1, err)
		}
		budget[strings.TrimSpace(line[:at])] = n
	}
	return budget, nil
}

func compare(budget, counts map[string]int) []string {
	seen := map[string]bool{}
	var drift []string
	for pkg, have := range counts {
		seen[pkg] = true
		want, ok := budget[pkg]
		switch {
		case !ok:
			drift = append(drift, fmt.Sprintf("%-16s %d exported symbols, and no budget line at all -- a new package is a new promise", pkg, have))
		case have != want:
			drift = append(drift, fmt.Sprintf("%-16s %d, budget says %d (%+d)", pkg, have, want, have-want))
		}
	}
	for pkg := range budget {
		if !seen[pkg] {
			drift = append(drift, fmt.Sprintf("%-16s in the budget but no longer measured -- was the package removed?", pkg))
		}
	}
	sort.Strings(drift)
	return drift
}

func report(counts map[string]int, syms map[string][]string, verbose bool) {
	pkgs := make([]string, 0, len(counts))
	for p := range counts {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)
	total := 0
	for _, p := range pkgs {
		fmt.Printf("%-16s %4d\n", p, counts[p])
		total += counts[p]
	}
	fmt.Printf("%-16s %4d\n", "TOTAL", total)
	if !verbose {
		return
	}
	for _, p := range pkgs {
		sort.Strings(syms[p])
		fmt.Printf("\n=== %s (%d) ===\n", p, counts[p])
		for _, s := range syms[p] {
			fmt.Println("  " + s)
		}
	}
}
