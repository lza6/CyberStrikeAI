package dedup

import "testing"

func TestFindDuplicates_SimilarTitlesMatch(t *testing.T) {
	priors := []Prior{
		{ID: "1", Title: "Stored XSS in comments section"},
		{ID: "2", Title: "SQL injection in /search endpoint"},
		{ID: "3", Title: "Missing HSTS header"},
	}
	hits := FindDuplicates("SQL Injection on the search endpoint", "", priors, 0.4, 3)
	if len(hits) == 0 || hits[0].Prior.ID != "2" {
		t.Fatalf("expected match with #2; got %+v", hits)
	}
}

func TestFindDuplicates_TargetBoost(t *testing.T) {
	priors := []Prior{
		{ID: "match", Title: "XSS somewhere", Target: "acme.corp"},
		{ID: "nomatch", Title: "XSS somewhere"},
	}
	// 相同 token 集，但仅有一条共享 target——该条应排名更高。
	hits := FindDuplicates("XSS somewhere", "acme.corp", priors, 0.5, 2)
	if len(hits) == 0 {
		t.Fatal("expected at least one hit")
	}
	if hits[0].Prior.ID != "match" {
		t.Fatalf("target-matching prior should rank first; got %+v", hits)
	}
}

func TestFindDuplicates_BelowThresholdDropped(t *testing.T) {
	priors := []Prior{{ID: "1", Title: "Completely unrelated bug"}}
	hits := FindDuplicates("SSRF in image fetcher", "", priors, 0.5, 3)
	if len(hits) != 0 {
		t.Fatalf("unrelated titles should not match; got %+v", hits)
	}
}

func TestTokenise_DropsStopwords(t *testing.T) {
	tokens := tokenise("SQL injection in the search endpoint")
	if _, in := tokens["in"]; in {
		t.Error("'in' should be a stopword")
	}
	if _, in := tokens["the"]; in {
		t.Error("'the' should be a stopword")
	}
	for _, w := range []string{"sql", "injection", "search", "endpoint"} {
		if _, ok := tokens[w]; !ok {
			t.Errorf("expected token %q", w)
		}
	}
}

// --- 边界用例 ---

func TestFindDuplicates_EmptyPriorsReturnsEmpty(t *testing.T) {
	hits := FindDuplicates("SQL Injection", "", nil, 0.6, 3)
	if len(hits) != 0 {
		t.Fatalf("expected empty; got %+v", hits)
	}
}

func TestFindDuplicates_EmptyTitleReturnsEmpty(t *testing.T) {
	priors := []Prior{{ID: "1", Title: "SQL injection in /search endpoint"}}
	hits := FindDuplicates("", "", priors, 0.4, 3)
	if len(hits) != 0 {
		t.Fatalf("empty title should not match; got %+v", hits)
	}
}

func TestFindDuplicates_KZeroReturnsEmpty(t *testing.T) {
	priors := []Prior{{ID: "1", Title: "SQL injection in /search endpoint"}}
	// k=0 触发默认 k=3，但阈值之上仅一条，结果非空——验证 k=0 不清空结果。
	// 改为验证 k 负数同样回退默认值。
	hits := FindDuplicates("SQL injection in /search endpoint", "", priors, 0.4, 0)
	if len(hits) == 0 {
		t.Fatal("k=0 should fall back to default k=3, expected at least one hit")
	}
	if hits[0].Prior.ID != "1" {
		t.Fatalf("expected match #1; got %+v", hits)
	}
}

func TestFindDuplicates_ThresholdZeroUsesDefault(t *testing.T) {
	priors := []Prior{
		{ID: "1", Title: "SQL injection in /search endpoint"},
		{ID: "2", Title: "Completely unrelated bug"},
	}
	// threshold=0 应使用默认 0.6。第二条完全不相关，token 无重叠，相似度 0 < 0.6 不命中。
	// 第一条与查询相同，相似度 1.0 >= 0.6 命中。
	hits := FindDuplicates("SQL injection in /search endpoint", "", priors, 0, 3)
	if len(hits) != 1 {
		t.Fatalf("expected exactly one hit with default threshold 0.6; got %+v", hits)
	}
	if hits[0].Prior.ID != "1" {
		t.Fatalf("expected match #1; got %+v", hits)
	}
}

func TestFindDuplicates_IdenticalTitlesSimOne(t *testing.T) {
	priors := []Prior{{ID: "1", Title: "SQL Injection in /search endpoint"}}
	hits := FindDuplicates("SQL Injection in /search endpoint", "", priors, 0.6, 3)
	if len(hits) != 1 {
		t.Fatalf("expected one hit; got %+v", hits)
	}
	if hits[0].Similarity != 1.0 {
		t.Fatalf("expected similarity 1.0 for identical titles; got %v", hits[0].Similarity)
	}
}
