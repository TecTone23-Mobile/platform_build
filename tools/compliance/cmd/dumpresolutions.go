// Copyright 2021 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"compliance"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var (
	conditions      = newMultiString("c", "License condition to resolve. (may be given multiple times)")
	graphViz        = flag.Bool("dot", false, "Whether to output graphviz (i.e. dot) format.")
	labelConditions = flag.Bool("label_conditions", false, "Whether to label target nodes with conditions.")
	stripPrefix     = flag.String("strip_prefix", "", "Prefix to remove from paths. i.e. path to root")

	failNoneRequested = fmt.Errorf("\nNo license metadata files requested")
	failNoLicenses = fmt.Errorf("No licenses found")
)

type context struct {
	conditions      []compliance.LicenseCondition
	graphViz        bool
	labelConditions bool
	stripPrefix     string
}

func init() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage: %s {options} file.meta_lic {file.meta_lic...}

Outputs a space-separated Target ActsOn Origin Condition tuple for each
resolution in the graph. When -dot flag given, outputs nodes and edges
in graphviz directed graph format.

If one or more '-c condition' conditions are given, outputs the
resolution for the union of the conditions. Otherwise, outputs the
resolution for all conditions.

In plain text mode, when '-label_conditions' is requested, the Target
and Origin have colon-separated license conditions appended:
i.e. target:condition1:condition2 etc.

Options:
`, filepath.Base(os.Args[0]))
		flag.PrintDefaults()
	}
}

// newMultiString creates a flag that allows multiple values in an array.
func newMultiString(name, usage string) *multiString {
	var f multiString
	flag.Var(&f, name, usage)
	return &f
}

// multiString implements the flag `Value` interface for multiple strings.
type multiString []string

func (ms *multiString) String() string     { return strings.Join(*ms, ", ") }
func (ms *multiString) Set(s string) error { *ms = append(*ms, s); return nil }

func main() {
	flag.Parse()

	// Must specify at least one root target.
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}

	lcs := make([]compliance.LicenseCondition, 0, len(*conditions))
	for _, name := range *conditions {
		lcs = append(lcs, compliance.RecognizedConditionNames[name])
	}
	ctx := &context{
		conditions:      lcs,
		graphViz:        *graphViz,
		labelConditions: *labelConditions,
		stripPrefix:     *stripPrefix,
	}
	_, err := dumpResolutions(ctx, os.Stdout, os.Stderr, flag.Args()...)
	if err != nil {
		if err == failNoneRequested {
			flag.Usage()
		}
		fmt.Fprintf(os.Stderr, "%s\n", err.Error())
		os.Exit(1)
	}
	os.Exit(0)
}

// dumpResolutions implements the dumpresolutions utility.
func dumpResolutions(ctx *context, stdout, stderr io.Writer, files ...string) (*compliance.LicenseGraph, error) {
	if len(files) < 1 {
		return nil, failNoneRequested
	}

	// Read the license graph from the license metadata files (*.meta_lic).
	licenseGraph, err := compliance.ReadLicenseGraph(os.DirFS("."), stderr, files)
	if err != nil {
		return nil, fmt.Errorf("Unable to read license metadata file(s) %q: %v\n", files, err)
	}
	if licenseGraph == nil {
		return nil, failNoLicenses
	}

	compliance.ResolveTopDownConditions(licenseGraph)
	cs := compliance.AllLicenseConditions
	if len(ctx.conditions) > 0 {
		cs = compliance.NewLicenseConditionSet()
		for _, c := range ctx.conditions {
			cs = cs.Plus(c)
		}
	}

	resolutions := compliance.WalkResolutionsForCondition(licenseGraph, cs)

	// nodes maps license metadata file names to graphViz node names when graphViz requested.
	nodes := make(map[string]string)
	n := 0

	// targetOut calculates the string to output for `target` adding `sep`-separated conditions as needed.
	targetOut := func(target *compliance.TargetNode, sep string) string {
		tOut := strings.TrimPrefix(target.Name(), ctx.stripPrefix)
		if ctx.labelConditions {
			conditions := target.LicenseConditions().Names()
			if len(conditions) > 0 {
				tOut += sep + strings.Join(conditions, sep)
			}
		}
		return tOut
	}

	// makeNode maps `target` to a graphViz node name.
	makeNode := func(target *compliance.TargetNode) {
		tName := target.Name()
		if _, ok := nodes[tName]; !ok {
			nodeName := fmt.Sprintf("n%d", n)
			nodes[tName] = nodeName
			fmt.Fprintf(stdout, "\t%s [label=\"%s\"];\n", nodeName, targetOut(target, "\\n"))
			n++
		}
	}

	// outputResolution prints a resolution in the requested format to `stdout`, where one can read
	// a resolution as `tname` resolves `oname`'s conditions named in `cnames`.
	// `tname` is the name of the target the resolution applies to.
	// `cnames` is the list of conditions to resolve.
	outputResolution := func(tname, aname string, cnames []string) {
		if ctx.graphViz {
			// ... one edge per line labelled with \\n-separated annotations.
			tNode := nodes[tname]
			aNode := nodes[aname]
			fmt.Fprintf(stdout, "\t%s -> %s [label=\"%s\"];\n", tNode, aNode, strings.Join(cnames, "\\n"))
		} else {
			// ... one edge per line with names in a colon-separated tuple.
			fmt.Fprintf(stdout, "%s %s %s\n", tname, aname, strings.Join(cnames, ":"))
		}
	}

	// Sort the resolutions by targetname for repeatability/stability.
	targets := resolutions.AttachesTo()
	sort.Sort(targets)

	// If graphviz output, start the directed graph.
	if ctx.graphViz {
		fmt.Fprintf(stdout, "strict digraph {\n\trankdir=LR;\n")
		for _, target := range targets {
			makeNode(target)
			rl := resolutions.Resolutions(target)
			sort.Sort(rl)
			for _, r := range rl {
				makeNode(r.ActsOn())
			}
		}
	}

	// Output the sorted targets.
	for _, target := range targets {
		var tname string
		if ctx.graphViz {
			tname = target.Name()
		} else {
			tname = targetOut(target, ":")
		}

		rl := resolutions.Resolutions(target)
		sort.Sort(rl)
		for _, r := range rl {
			var aname string
			if ctx.graphViz {
				aname = r.ActsOn().Name()
			} else {
				aname = targetOut(r.ActsOn(), ":")
			}

			// cnames accumulates the list of condition names originating at a single origin that apply to `target`.
			cnames := r.Resolves().Names()

			// Output 1 line for each attachesTo+actsOn combination.
			outputResolution(tname, aname, cnames)
		}
	}
	// If graphViz output, rank the root nodes together, and complete the directed graph.
	if ctx.graphViz {
		fmt.Fprintf(stdout, "\t{rank=same;")
		for _, f := range files {
			fName := f
			if !strings.HasSuffix(fName, ".meta_lic") {
				fName += ".meta_lic"
			}
			if fNode, ok := nodes[fName]; ok {
				fmt.Fprintf(stdout, " %s", fNode)
			}
		}
		fmt.Fprintf(stdout, "}\n}\n")
	}
	return licenseGraph, nil
}
