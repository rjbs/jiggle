use v5.36;

use Test::More;

use lib 'lib', 't/lib';

use Jiggle::Query;
use Jiggle::Sheet;
use Jiggle::TestLibrary;

my ($library) = library_with(
  photos => [
    { id => 'aaaa0001', title => 'First', taken => '2008-01-06T19:36:11', tags => [ 'high-st' ] },
    { id => 'bbbb0002', title => 'Second', taken => '2026-07-16T10:00:00', visibility => 'private' },
    { id => 'cccc0003', title => 'Undated' },
  ],
  albums => [ { slug => 'trip', title => 'Trip & Co.', photos => [ 'aaaa0001' ] } ],
);

sub sheet_for ($terms, %arg) {
  my @photos = Jiggle::Query->new({ library => $library, terms => $terms })->photos;
  return Jiggle::Sheet->new({ library => $library, label => "@$terms" })->html(\@photos, \%arg);
}

sub sheet_has ($desc, $html, @want) {
  like($html, $_, "$desc: $_") for @want;
}

my $all = sheet_for([ 'all' ]);
sheet_has('every photo', $all,
  qr{<title>all \(3\)</title>},
  qr{<b>First</b><br>2008-01-06},
  qr{<i>Trip &amp; Co\.</i><br>high-st},
  qr{2026-07-16 <span class="flag private">private</span><br>},
  qr{<b>Undated</b><br>undated},
  qr{href="file://[^"]*/meta/aa/aaaa0001\.toml">aaaa0001</a>},
  qr{src="file://[^"]*/derived/aa/aaaa0001/h480\.webp"},
);
unlike($all, qr{<h2>}, 'no groups unless asked');

my $private = sheet_for([ 'private' ]);
unlike($private, qr{aaaa0001}, 'only what the query matches');

my $by_year = sheet_for([ 'all' ], group => 'year');
sheet_has('grouped by year', $by_year,
  qr{<h2>2008 <span>1</span></h2>.*<h2>2026 <span>1</span></h2>.*<h2>undated <span>1</span></h2>}s);

ok(! eval { sheet_for([ 'all' ], group => 'colour'); 1 }, 'an unknown grouping is an error');

done_testing;
