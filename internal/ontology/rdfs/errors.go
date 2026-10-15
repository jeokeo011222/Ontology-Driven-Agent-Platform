package rdfs

import (
	"odap/internal/ontology/graphstore"
	"odap/internal/platform/errors"
)

// TypeTripleKind 是四类"类型三元组"的类别（第十三篇 §14.1）。
type TypeTripleKind string

const (
	TypeSubClassOf    TypeTripleKind = "RDFS_SUBCLASSOF"
	TypeSubPropertyOf TypeTripleKind = "RDFS_SUBPROPERTYOF"
	TypeDomain        TypeTripleKind = "RDFS_DOMAIN"
	TypeRange         TypeTripleKind = "RDFS_RANGE"
)

// PredicateIRI 返回该类别对应的谓词 IRI。
func (k TypeTripleKind) PredicateIRI() string {
	switch k {
	case TypeSubClassOf:
		return RDFSSubClassOf
	case TypeSubPropertyOf:
		return RDFSSubPropertyOf
	case TypeDomain:
		return RDFSDomain
	case TypeRange:
		return RDFSRange
	default:
		return ""
	}
}

// TypeTriple 是一条类型三元组（subClassOf / subPropertyOf / domain / range）。
type TypeTriple struct {
	Kind    TypeTripleKind
	Subject graphstore.Term
	Object  graphstore.Term
}

// ClassifyTypeTriple 识别类型三元组；不是四类之一时返回 ok=false。
func ClassifyTypeTriple(t graphstore.Triple) (TypeTriple, bool) {
	if t.P.Kind != graphstore.KindIRI {
		return TypeTriple{}, false
	}
	var kind TypeTripleKind
	switch t.P.Lexical {
	case RDFSSubClassOf:
		kind = TypeSubClassOf
	case RDFSSubPropertyOf:
		kind = TypeSubPropertyOf
	case RDFSDomain:
		kind = TypeDomain
	case RDFSRange:
		kind = TypeRange
	default:
		return TypeTriple{}, false
	}
	return TypeTriple{Kind: kind, Subject: t.S, Object: t.O}, true
}

// Validate 校验类型三元组的合法形态：主语不能是字面量，宾语不能是字面量。
func (tt TypeTriple) Validate() error {
	if tt.Kind.PredicateIRI() == "" {
		return errors.Ef(errors.CodeOntologyValidationFailed, "未知的类型三元组类别 %q", tt.Kind)
	}
	if tt.Subject.Kind == graphstore.KindLiteral {
		return errors.Ef(errors.CodeOntologyValidationFailed,
			"%s 的主语不能是字面量（非法 RDF：字面量不可作主语）", tt.Kind)
	}
	if !isResource(tt.Subject.Kind) {
		return errors.Ef(errors.CodeOntologyValidationFailed,
			"%s 的主语 term_kind=%d 非法（仅允许 IRI / BlankNode）", tt.Kind, tt.Subject.Kind)
	}
	if tt.Object.Kind == graphstore.KindLiteral {
		return errors.Ef(errors.CodeOntologyValidationFailed,
			"%s 的宾语不能是字面量（类型/类必须是资源）", tt.Kind)
	}
	if !isResource(tt.Object.Kind) {
		return errors.Ef(errors.CodeOntologyValidationFailed,
			"%s 的宾语 term_kind=%d 非法（仅允许 IRI / BlankNode）", tt.Kind, tt.Object.Kind)
	}
	return nil
}

// ValidateTriple 校验任意三元组的 RDF 合法性：
// 主语不能是字面量、谓词必须是 IRI、非字面值词项不得携带 datatype/language。
func ValidateTriple(t graphstore.Triple) error {
	if !isValidKind(t.S.Kind) {
		return errors.Ef(errors.CodeOntologyValidationFailed, "主语 term_kind=%d 非法", t.S.Kind)
	}
	if !isValidKind(t.P.Kind) {
		return errors.Ef(errors.CodeOntologyValidationFailed, "谓词 term_kind=%d 非法", t.P.Kind)
	}
	if !isValidKind(t.O.Kind) {
		return errors.Ef(errors.CodeOntologyValidationFailed, "宾语 term_kind=%d 非法", t.O.Kind)
	}
	if t.S.Kind == graphstore.KindLiteral {
		return errors.E(errors.CodeOntologyValidationFailed, "字面量不能作主语（非法 RDF）")
	}
	if t.P.Kind != graphstore.KindIRI {
		return errors.E(errors.CodeOntologyValidationFailed, "谓词必须是 IRI")
	}
	for name, term := range map[string]graphstore.Term{"主语": t.S, "谓词": t.P, "宾语": t.O} {
		if term.Kind != graphstore.KindLiteral && (term.Datatype != "" || term.Lang != "") {
			return errors.Ef(errors.CodeOntologyValidationFailed,
				"%s 不是字面值，但携带了 datatype/language", name)
		}
	}
	if t.O.Kind == graphstore.KindLiteral && t.O.Datatype != "" && t.O.Lang != "" {
		return errors.E(errors.CodeOntologyValidationFailed, "字面值不能同时具有 datatype 与 language")
	}
	if t.S.Lexical == "" || t.P.Lexical == "" || t.O.Lexical == "" {
		return errors.E(errors.CodeOntologyValidationFailed, "词项 lexical_form 不能为空")
	}
	return nil
}

func isValidKind(k uint8) bool {
	return k == graphstore.KindIRI || k == graphstore.KindBlankNode || k == graphstore.KindLiteral
}

func isResource(k uint8) bool {
	return k == graphstore.KindIRI || k == graphstore.KindBlankNode
}
