package auth

import "testing"

func TestHashVerify(t *testing.T) {
	hash, err := HashPassword("секрет-123")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if hash == "секрет-123" {
		t.Fatal("hash equals password")
	}
	ok, err := VerifyPassword("секрет-123", hash)
	if err != nil || !ok {
		t.Fatalf("verify correct: ok=%v err=%v", ok, err)
	}
	ok, err = VerifyPassword("wrong", hash)
	if err != nil {
		t.Fatalf("verify wrong err: %v", err)
	}
	if ok {
		t.Fatal("wrong password accepted")
	}
}

func TestVerifyInvalidHash(t *testing.T) {
	if _, err := VerifyPassword("x", "not-a-hash"); err == nil {
		t.Fatal("expected error")
	}
}

func TestHashesAreSalted(t *testing.T) {
	h1, _ := HashPassword("same")
	h2, _ := HashPassword("same")
	if h1 == h2 {
		t.Fatal("hashes should differ due to random salt")
	}
}
