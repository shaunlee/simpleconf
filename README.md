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
BenchmarkGet-4      30000000           50.7 ns/op
BenchmarkSet-4      10000000           201 ns/op
BenchmarkDel-4      20000000           107 ns/op
BenchmarkClone-4     2000000           769 ns/op
```

wrk write to 2 nodes

```
Running 10s test @ http://127.0.0.1:3001/db/bench
  2 threads and 10 connections
  Thread Stats   Avg      Stdev     Max   +/- Stdev
    Latency     2.16ms    1.55ms  23.35ms   85.99%
    Req/Sec     2.53k   146.17     2.96k    69.50%
  50289 requests in 10.00s, 6.43MB read
Requests/sec:   5027.95
Transfer/sec:    657.95KB
```

wrk read from 1 node

```
Running 10s test @ http://127.0.0.1:3001/db/bench
  2 threads and 10 connections
  Thread Stats   Avg      Stdev     Max   +/- Stdev
    Latency   426.75us    0.97ms  13.59ms   91.42%
    Req/Sec    39.48k     4.79k   53.95k    67.50%
  786578 requests in 10.01s, 105.02MB read
Requests/sec:  78548.95
Transfer/sec:     10.49MB
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
