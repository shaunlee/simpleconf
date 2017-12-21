# simpleconf

Simple way to read write configuration in a cluster, without single point of failure(SPOF).

## usage

```bash
./simpleconf

  --ignore-peers true
        ignore peers as of first node starting, default is false
```

## config.yml

- db: appendonly database file
- listen: frontend endpoint
- peers.listen: endpoint of data synchronization
- peers.addresses: cluster's peer nodes

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
BenchmarkGet-4      30000000           50.7 ns/op   19,723,865 tps
BenchmarkSet-4      10000000           201 ns/op    4,975,124 tps
BenchmarkDel-4      20000000           107 ns/op    9,345,794 tps
BenchmarkClone-4     2000000           769 ns/op    1,300,390 tps
```

## interfaces

#### Get whole configurations

`GET /db`

Returns raw JSON, in case of dump the database, don't use it often

#### Get values with key path

`GET /db/{key.path}`

Returns raw JSON, use key path as fine-grained as possible

#### Set values by key path

`POST /db/{key.path}`

Post any of raw JSON body

#### Delete values key path

`DELETE /db/{key.path}`

#### Clone values between key path

`POST /clone/{from.key.path}/{to.key.path}`

#### Rewrite appendonly database file

`POST /rewriteaof`
