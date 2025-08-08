# simpleconf

Simple way to read and write configurations in a cluster, without single point of failure(SPOF).

## config.yml

- db: appendonly database file
- listen: frontend endpoint

## example

```bash
echo '2017' | http http://localhost:23456/db/product.year
echo '"Demo"' | http http://localhost:23456/db/product.name
echo 'false' | http http://localhost:23456/db/product.is_expired

http http://localhost:23456/db/product
{
    "is_expired": false, 
    "name": "Demo", 
    "year": 2017
}

http delete http://localhost:23456/db/product.is_expired

http http://localhost:23456/db/product
{
    "name": "Demo", 
    "year": 2017
}
```

## benchmarks

```
cpu: AMD Ryzen 9 5900HX with Radeon Graphics
BenchmarkGet-16      	35865764	       31.97 ns/op	       0 B/op	       0 allocs/op
BenchmarkSet-16      	 7825952	       153.1 ns/op	      96 B/op	       3 allocs/op
BenchmarkDel-16      	12272269	       96.00 ns/op	      80 B/op	       3 allocs/op
BenchmarkClone-16    	 5811598	       205.7 ns/op	     168 B/op	       4 allocs/op
```

```
cpu: AMD Ryzen 9 5900HX with Radeon Graphics
BenchmarkTcpGet-16      	  227306	      4811 ns/op
BenchmarkTcpSet-16      	  249592	      5345 ns/op
BenchmarkTcpDel-16      	  263331	      5299 ns/op
BenchmarkTcpClone-16    	  223882	      5059 ns/op
```

wrk get

```
Running 10s test @ http://127.0.0.1:23456/db/bench
  2 threads and 10 connections
  Thread Stats   Avg      Stdev     Max   +/- Stdev
    Latency    40.06us   10.91us 738.00us   76.41%
    Req/Sec   104.03k     4.36k  113.08k    65.35%
  2091039 requests in 10.10s, 295.14MB read
Requests/sec: 207040.39
Transfer/sec:     29.22MB
```

wrk set

```
Running 10s test @ http://127.0.0.1:23456/db/bench
  2 threads and 10 connections
  Thread Stats   Avg      Stdev     Max   +/- Stdev
    Latency    66.40us   34.08us   1.21ms   90.89%
    Req/Sec    72.18k     1.03k   75.19k    68.81%
  1450633 requests in 10.10s, 172.93MB read
Requests/sec: 143629.05
Transfer/sec:     17.12MB
```

wrk delete

```
Running 10s test @ http://127.0.0.1:23456/db/bench
  2 threads and 10 connections
  Thread Stats   Avg      Stdev     Max   +/- Stdev
    Latency    53.39us   41.82us   3.48ms   96.62%
    Req/Sec    86.50k     2.24k   95.54k    69.00%
  1721124 requests in 10.00s, 205.17MB read
Requests/sec: 172110.01
Transfer/sec:     20.52MB
```

## interfaces

#### Get whole configurations

HTTP:
`GET /db`

TCP:
`=`

Returns raw JSON, in case of dump the database, don't use it often

#### Get values with key path

HTTP:
`GET /db/{key.path}`

TCP:
`=key.path`

Returns raw JSON, use key path as fine-grained as possible

#### Set values by key path

HTTP:
`PUT /db/{key.path} {"name": "Demo"}`

TCP:
```
+key.path
{"name": "Demo"}
```

Put any of raw JSON body

#### Delete values key path

`DELETE /db/{key.path}`

TCP:
`-key.path`

#### Clone values between key path

HTTP:
`POST /clone/{from.key.path}/{to.key.path}`

TCP:
```
<from.key.path
>to.key.path
```

#### Rewrite appendonly database file

HTTP:
`POST /vacuum`

TCP:
`*`

#### TCP ping

`PING`

## License 

MIT
