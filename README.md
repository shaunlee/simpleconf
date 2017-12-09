# simpleconf

Most simple way to read write configuration.

## usage

```bash
./simpleconf

  -db string
        Appendonly database filename (default "data.aof")
  -listen string
        Http server address listen on (default ":3000")
```

## example

```bash
echo '2017' | http http://localhost/db/product.year
echo '"Demo"' | http http://localhost/db/product.name
echo 'false' | http http://localhost/db/product.is_expired

http http://localhost/db/product
{
    "is_expired": false, 
    "name": "Demo", 
    "year": 2017
}

http delete http://localhost/db/product.is_expired

http http://localhost/db/product
{
    "name": "Demo", 
    "year": 2017
}
```

## benchmarks

```
BenchmarkGet-4      30000000            50.7 ns/op
BenchmarkSet-4      10000000           201 ns/op
BenchmarkDel-4      20000000           107 ns/op
BenchmarkClone-4     2000000           769 ns/op
```

## interfaces

#### Get whole configurations

`GET /db`

Returns raw JSON

#### Get values with key path

`GET /db/{key.path}`

Returns raw JSON

#### Set values by key path

`POST /db/{key.path}`

Post raw JSON body

#### Delete values key path

`DELETE /db/{key.path}`

#### Clone values between key path

`POST /clone/{from.key.path}/{to.key.path}`
