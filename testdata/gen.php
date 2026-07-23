<?php
// テストベクタの生成 / 検証用スクリプト。
// decode_test.go / encode_test.go に埋め込んだ期待値は本スクリプトの出力から採取した。
//   php testdata/gen.php
declare(strict_types=1);

$vectors = [
    'null'          => null,
    'true'          => true,
    'false'         => false,
    'int'           => 123,
    'negative int'  => -123,
    'int64 min'     => PHP_INT_MIN,
    'float 0.1'     => 0.1,
    'float 1/3'     => 1 / 3,
    'float int'     => 1.0,
    'INF'           => INF,
    '-INF'          => -INF,
    'NAN'           => NAN,
    'string'        => 'abc',
    'multibyte'     => 'プロレス',
    'empty string'  => '',
    'list'          => ['a', 'b'],
    'assoc'         => ['k' => 1, 2 => 'v'],
    'gap keys'      => [0 => 'a', 5 => 'b'],
    'negative key'  => [-1 => 'a'],
    'numeric str key' => ['5' => 'a'],
    'nested'        => ['xs' => [1, 2]],
    'wp attachment' => [
        'width' => 1200, 'height' => 630, 'file' => '2024/01/example.jpg', 'filesize' => 123456,
        'sizes' => ['thumbnail' => [
            'file' => 'example.jpg', 'width' => 150, 'height' => 150,
            'mime_type' => 'image/jpeg', 'filesize' => 5000,
        ]],
    ],
];

foreach ($vectors as $name => $v) {
    printf("%-16s: %s\n", $name, serialize($v));
}

class P { public $pub = 1; protected $pro = 2; private $pri = 3; }
printf("%-16s: %s\n", 'object mangle', str_replace("\0", '\\0', serialize(new P())));
