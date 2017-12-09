# simpleconf

Most simple configuration way.

## interfaces

#### Get whole configurations

`GET /db`

Returns raw JSON

#### Get values with key path

`GET /db/{key.path}`

Returns raw JSON

#### Set values by key path

`POST /db/{key.path}

Post raw JSON body

#### Delete values key path

`DELETE /db/{key.path}`

#### Clone values between key path

`POST /clone/{from.key.path}/{to.key.path}`

## benchmarks

```
BenchmarkGet-4      10000000           151 ns/op
BenchmarkSet-4       2000000           648 ns/op
BenchmarkDel-4       3000000           478 ns/op
BenchmarkClone-4     1000000          1305 ns/op
```
