package rdfs

import (
	"testing"

	"odap/internal/ontology/graphstore"
	"odap/internal/platform/errors"
)

func TestClassifyTypeTriple(t *testing.T) {
	cases := []struct {
		predicate string
		wantKind  TypeTripleKind
		wantOK    bool
	}{
		{RDFSSubClassOf, TypeSubClassOf, true},
		{RDFSSubPropertyOf, TypeSubPropertyOf, true},
		{RDFSDomain, TypeDomain, true},
		{RDFSRange, TypeRange, true},
		{"https://ex/other", "", false},
	}
	for _, c := range cases {
		t.Run(c.predicate, func(t *testing.T) {
			tri := graphstore.Triple{S: tIRI("https://ex/a"), P: tIRI(c.predicate), O: tIRI("https://ex/b")}
			got, ok := ClassifyTypeTriple(tri)
			if ok != c.wantOK {
				t.Fatalf("ok=%v，期望 %v", ok, c.wantOK)
			}
			if ok && got.Kind != c.wantKind {
				t.Fatalf("kind=%s，期望 %s", got.Kind, c.wantKind)
			}
			if ok && got.Kind.PredicateIRI() != c.predicate {
				t.Fatalf("PredicateIRI=%s，期望 %s", got.Kind.PredicateIRI(), c.predicate)
			}
		})
	}

	// 谓词不是 IRI 时不应被识别为类型三元组。
	if _, ok := ClassifyTypeTriple(graphstore.Triple{
		S: tIRI("https://ex/a"), P: tLit(RDFSSubClassOf), O: tIRI("https://ex/b"),
	}); ok {
		t.Fatal("非 IRI 谓词不应识别为类型三元组")
	}
}

func TestTypeTripleValidate(t *testing.T) {
	tests := []struct {
		name    string
		tt      TypeTriple
		wantErr bool
	}{
		{
			name: "合法",
			tt:   TypeTriple{Kind: TypeSubClassOf, Subject: tIRI("https://ex/a"), Object: tIRI("https://ex/b")},
		},
		{
			name:    "字面量作主语（非法）",
			tt:      TypeTriple{Kind: TypeSubClassOf, Subject: tLit("a"), Object: tIRI("https://ex/b")},
			wantErr: true,
		},
		{
			name:    "字面量作宾语（非法）",
			tt:      TypeTriple{Kind: TypeDomain, Subject: tIRI("https://ex/p"), Object: tLit("c")},
			wantErr: true,
		},
		{
			name:    "未知类别（非法）",
			tt:      TypeTriple{Kind: "RDFS_UNKNOWN", Subject: tIRI("https://ex/a"), Object: tIRI("https://ex/b")},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.tt.Validate()
			if tt.wantErr {
				if err == nil {
					t.Fatal("期望错误，实际为 nil")
				}
				if appErr := errors.As(err); appErr.Code != errors.CodeOntologyValidationFailed {
					t.Fatalf("错误码应为 %s，实际 %s", errors.CodeOntologyValidationFailed, appErr.Code)
				}
				return
			}
			if err != nil {
				t.Fatalf("非预期错误: %v", err)
			}
		})
	}
}

func TestValidateTriple(t *testing.T) {
	tests := []struct {
		name    string
		tri     graphstore.Triple
		wantErr bool
	}{
		{
			name: "合法三元组",
			tri:  tr(tIRI("https://ex/alice"), tIRI("https://ex/name"), tLit("Alice")),
		},
		{
			name:    "字面量作主语",
			tri:     tr(tLit("Alice"), tIRI("https://ex/name"), tIRI("https://ex/bob")),
			wantErr: true,
		},
		{
			name:    "谓词为字面量",
			tri:     tr(tIRI("https://ex/a"), tLit("p"), tIRI("https://ex/b")),
			wantErr: true,
		},
		{
			name:    "非字面值携带 datatype",
			tri:     graphstore.Triple{S: tIRI("https://ex/a"), P: tIRI("https://ex/p"), O: graphstore.Term{Kind: graphstore.KindIRI, Lexical: "https://ex/b", Datatype: "x"}},
			wantErr: true,
		},
		{
			name:    "字面值同时带 datatype 与 language",
			tri:     graphstore.Triple{S: tIRI("https://ex/a"), P: tIRI("https://ex/p"), O: graphstore.Term{Kind: graphstore.KindLiteral, Lexical: "x", Datatype: "dt", Lang: "en"}},
			wantErr: true,
		},
		{
			name:    "空 lexical",
			tri:     graphstore.Triple{S: tIRI(""), P: tIRI("https://ex/p"), O: tIRI("https://ex/b")},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateTriple(tt.tri)
			if tt.wantErr && err == nil {
				t.Fatal("期望错误，实际为 nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("非预期错误: %v", err)
			}
		})
	}
}

func TestValidateTripleAcceptsBlankNodes(t *testing.T) {
	tri := graphstore.Triple{
		S: graphstore.Term{Kind: graphstore.KindBlankNode, Lexical: "b0"},
		P: tIRI("https://ex/name"),
		O: tLit("Alice"),
	}
	if err := ValidateTriple(tri); err != nil {
		t.Fatalf("空白节点作主语应合法: %v", err)
	}
}
