package db

import "testing"

//const dbfile = "test.aof"

func BenchmarkGet(b *testing.B) {
	//Init(dbfile)
	//defer Close()

	setonly("bench", "mark")
	for i := 0; i < b.N; i++ {
		Get("bench")
	}
}

func BenchmarkSet(b *testing.B) {
	//Init(dbfile)
	//defer Close()

	for i := 0; i < b.N; i++ {
		setonly("bench", "mark")
	}
}

func BenchmarkDel(b *testing.B) {
	//Init(dbfile)
	//defer Close()

	for i := 0; i < b.N; i++ {
		delonly("bench")
	}
}

func BenchmarkClone(b *testing.B) {
	//Init(dbfile)
	//defer Close()

	setonly("bench", "mark")
	for i := 0; i < b.N; i++ {
		cponly("bench", "mark")
	}
}
