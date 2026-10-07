use v5.36;

use Test::More;

use lib 'lib';

use Jiggle::Album;

sub album_photos_are ($desc, $listed, $want, $had_duplicates) {
  my $album = Jiggle::Album->new({ slug => 'a', title => 'A', photos => $listed });
  is_deeply($album->photos, $want, "$desc: photos");
  is(!! $album->had_duplicate_photos, !! $had_duplicates, "$desc: had duplicates?");
}

album_photos_are('distinct',  [ qw( a b c ) ],     [ qw( a b c ) ], 0);
album_photos_are('one twice', [ qw( a b a c ) ],   [ qw( a b c ) ], 1);
album_photos_are('empty',     [ ],                 [ ],             0);

done_testing;
