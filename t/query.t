use v5.36;

use Test::More;

use lib 'lib', 't/lib';

use Jiggle::Query;
use Jiggle::TestLibrary;

my ($library) = library_with(
  photos => [
    { id => 'new00001', visibility => 'pending', taken => '2026-09-01T10:00:00' },
    { id => 'new00002', visibility => 'pending', taken => '2026-08-01T10:00:00' },
    { id => 'old00001', taken => '2008-01-06T19:36:11', tags => [ 'High St' ] },
    { id => 'old00002', taken => '2008-04-01T12:00:00', tags => [ 'high-st', 'oslo' ] },
    { id => 'undated1', visibility => 'private' },
  ],
  albums => [
    { slug => 'oslo', title => 'Oslo', photos => [ 'old00002', 'old00001' ] },
  ],
);

sub query_selects ($terms, $want) {
  my $query = Jiggle::Query->new({ library => $library, terms => $terms });
  is_deeply([ map {; $_->id } $query->photos ], $want, "query: @$terms");
}

sub query_fails ($terms, $want_error) {
  my $ok = eval { Jiggle::Query->new({ library => $library, terms => $terms }); 1 };
  like($ok ? '' : $@, $want_error, "query fails: @$terms");
}

query_selects([ 'pending' ],            [ 'new00002', 'new00001' ]);
query_selects([ 'private' ],            [ 'undated1' ]);
query_selects([ 'public' ],             [ 'old00001', 'old00002' ]);
query_selects([ 'pending', 'public' ],  [ ]);
query_selects([ 'all' ],                [ 'old00001', 'old00002', 'new00002', 'new00001', 'undated1' ]);
query_selects([ 'tag:high-st' ],        [ 'old00001', 'old00002' ]);
query_selects([ 'tag:High St' ],        [ 'old00001', 'old00002' ]);
query_selects([ 'album:oslo' ],         [ 'old00002', 'old00001' ]);
query_selects([ 'album:oslo', 'tag:oslo' ], [ 'old00002' ]);
query_selects([ 'year:2026' ],          [ 'new00002', 'new00001' ]);
query_selects([ 'id:old00002', 'id:undated1' ], [ 'old00002', 'undated1' ]);
query_selects([ 'tag:nothing' ],        [ ]);
query_selects([ 'pending', 'limit:1' ], [ 'new00002' ]);
query_selects([ 'all', 'limit:2' ],     [ 'old00001', 'old00002' ]);
query_selects([ 'album:oslo', 'limit:1' ], [ 'old00002' ]);
query_selects([ 'tag:oslo', 'limit:5' ],   [ 'old00002' ]);

query_fails([ ],               qr/at least one term/);
query_fails([ 'pendng' ],      qr/unknown query term: pendng/);
query_fails([ 'album:nope' ],  qr/no album named nope/);
query_fails([ 'year:08' ],     qr/unknown query term/);
query_fails([ 'limit:0' ],     qr/unknown query term/);
query_fails([ 'limit:1', 'limit:2' ], qr/limit given more than once/);

done_testing;
