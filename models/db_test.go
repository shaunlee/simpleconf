package models

import "testing"

const dbfile = "data.aof"

func BenchmarkGet(b *testing.B) {
	InitDb(dbfile)
	defer FreeDb()

	setonly("bench", "mark")
	for i := 0; i < b.N; i++ {
		Get("bench")
	}
}

func BenchmarkSet(b *testing.B) {
	InitDb(dbfile)
	defer FreeDb()

	for i := 0; i < b.N; i++ {
		setonly("bench", "mark")
	}
}

func BenchmarkDel(b *testing.B) {
	InitDb(dbfile)
	defer FreeDb()

	for i := 0; i < b.N; i++ {
		delonly("bench")
	}
}

func BenchmarkClone(b *testing.B) {
	InitDb(dbfile)
	defer FreeDb()

	setonly("bench", "mark")
	for i := 0; i < b.N; i++ {
		cponly("bench", "mark")
	}
}
