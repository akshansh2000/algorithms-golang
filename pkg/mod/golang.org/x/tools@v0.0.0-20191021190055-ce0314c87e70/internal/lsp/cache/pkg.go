// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cache

import (
	"context"
	"go/ast"
	"go/types"
	"sync"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/internal/lsp/protocol"
	"golang.org/x/tools/internal/lsp/source"
	"golang.org/x/tools/internal/span"
	errors "golang.org/x/xerrors"
)

// pkg contains the type information needed by the source package.
type pkg struct {
	snapshot *snapshot

	// ID and package path have their own types to avoid being used interchangeably.
	id      packageID
	pkgPath packagePath
	mode    source.ParseMode

	files      []source.ParseGoHandle
	errors     []source.Error
	imports    map[packagePath]*pkg
	types      *types.Package
	typesInfo  *types.Info
	typesSizes types.Sizes

	diagMu      sync.Mutex
	diagnostics map[*analysis.Analyzer][]source.Diagnostic
}

// Declare explicit types for package paths and IDs to ensure that we never use
// an ID where a path belongs, and vice versa. If we confused the two, it would
// result in confusing errors because package IDs often look like package paths.
type packageID string
type packagePath string

func (p *pkg) Snapshot() source.Snapshot {
	return p.snapshot
}

func (p *pkg) ID() string {
	return string(p.id)
}

func (p *pkg) PkgPath() string {
	return string(p.pkgPath)
}

func (p *pkg) Files() []source.ParseGoHandle {
	return p.files
}

func (p *pkg) File(uri span.URI) (source.ParseGoHandle, error) {
	for _, ph := range p.Files() {
		if ph.File().Identity().URI == uri {
			return ph, nil
		}
	}
	return nil, errors.Errorf("no ParseGoHandle for %s", uri)
}

func (p *pkg) GetSyntax(ctx context.Context) []*ast.File {
	var syntax []*ast.File
	for _, ph := range p.files {
		file, _, _, err := ph.Cached(ctx)
		if err == nil {
			syntax = append(syntax, file)
		}
	}
	return syntax
}

func (p *pkg) GetErrors() []source.Error {
	return p.errors
}

func (p *pkg) GetTypes() *types.Package {
	return p.types
}

func (p *pkg) GetTypesInfo() *types.Info {
	return p.typesInfo
}

func (p *pkg) GetTypesSizes() types.Sizes {
	return p.typesSizes
}

func (p *pkg) IsIllTyped() bool {
	return p.types == nil || p.typesInfo == nil || p.typesSizes == nil
}

func (p *pkg) GetImport(ctx context.Context, pkgPath string) (source.Package, error) {
	if imp := p.imports[packagePath(pkgPath)]; imp != nil {
		return imp, nil
	}
	// Don't return a nil pointer because that still satisfies the interface.
	return nil, errors.Errorf("no imported package for %s", pkgPath)
}

func (p *pkg) SetDiagnostics(a *analysis.Analyzer, diags []source.Diagnostic) {
	p.diagMu.Lock()
	defer p.diagMu.Unlock()
	if p.diagnostics == nil {
		p.diagnostics = make(map[*analysis.Analyzer][]source.Diagnostic)
	}
	p.diagnostics[a] = diags
}

func (p *pkg) FindDiagnostic(pdiag protocol.Diagnostic) (*source.Diagnostic, error) {
	p.diagMu.Lock()
	defer p.diagMu.Unlock()

	for a, diagnostics := range p.diagnostics {
		if a.Name != pdiag.Source {
			continue
		}
		for _, d := range diagnostics {
			if d.Message != pdiag.Message {
				continue
			}
			if protocol.CompareRange(d.Range, pdiag.Range) != 0 {
				continue
			}
			return &d, nil
		}
	}
	return nil, errors.Errorf("no matching diagnostic for %v", pdiag)
}

func (p *pkg) FindFile(ctx context.Context, uri span.URI) (source.ParseGoHandle, source.Package, error) {
	// Special case for ignored files.
	if p.snapshot.view.Ignore(uri) {
		return p.snapshot.view.findIgnoredFile(ctx, uri)
	}

	queue := []*pkg{p}
	seen := make(map[string]bool)

	for len(queue) > 0 {
		pkg := queue[0]
		queue = queue[1:]
		seen[pkg.ID()] = true

		for _, ph := range pkg.files {
			if ph.File().Identity().URI == uri {
				return ph, pkg, nil
			}
		}
		for _, dep := range pkg.imports {
			if !seen[dep.ID()] {
				queue = append(queue, dep)
			}
		}
	}
	return nil, nil, errors.Errorf("no file for %s", uri)
}
