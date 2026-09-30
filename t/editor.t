use v5.36;

use Test::More;
use Test::Mojo;

use lib 'lib', 't/lib';

use Jiggle::Editor;
use Jiggle::TestLibrary;

sub editor_for (%arg) {
  my $ids = delete $arg{ids};
  my ($library) = library_with(%arg);
  my $editor = Jiggle::Editor->new({ library => $library, ids => $ids });
  return ($editor, Test::Mojo->new($editor->app));
}

# A client that has presented the token, and so holds the cookie.
sub signed_in (%arg) {
  my ($editor, $t) = editor_for(%arg);
  $t->get_ok('/?token=' . $editor->token)->status_is(302);
  return ($editor, $t);
}

sub refused_ok ($t, $path) {
  $t->get_ok($path)->status_is(403, "refused: $path");
}

subtest 'nothing without the token' => sub {
  my ($editor, $t) = editor_for(photos => [ { id => 'aaaa0001' } ], ids => [ 'aaaa0001' ]);

  refused_ok($t, $_) for '/', '/api/batch', '/r/aaaa0001/h480.webp', '/static/editor.js';
  $t->get_ok('/?token=wrong')->status_is(403, 'a wrong token is refused');
  refused_ok($t, '/api/batch');

  $t->get_ok('/?token=' . $editor->token)->status_is(302, 'the right token')
    ->header_like('Set-Cookie', qr/SameSite=Strict/i, '...sets a strict cookie');
  $t->get_ok('/')->status_is(200, '...which lets the page load')
    ->content_like(qr/<title>/);
  $t->get_ok('/api/batch')->status_is(200, '...and the data');
};

subtest 'the batch, as it is on disk' => sub {
  my ($editor, $t) = signed_in(
    photos => [
      { id => 'aaaa0001', title => 'first', tags => [ 'x' ], pending => 1,
        location => { lat => 40, lon => -75, private => 1 } },
      { id => 'bbbb0002', title => 'second', visibility => 'private' },
      { id => 'cccc0003', title => 'not in the batch' },
    ],
    albums => [ { slug => 'trip', title => 'Trip', photos => [ 'bbbb0002', 'cccc0003' ] } ],
    ids => [ 'bbbb0002', 'aaaa0001' ],
  );

  $t->get_ok('/api/batch')->status_is(200)
    ->json_is('/photos/0/id', 'bbbb0002', 'in batch order')
    ->json_is('/photos/0/albums', [ 'trip' ], 'album membership')
    ->json_is('/photos/0/visibility', 'private')
    ->json_is('/photos/1/id', 'aaaa0001')
    ->json_is('/photos/1/pending', 1, 'pending')
    ->json_is('/photos/1/tags', [ 'x' ])
    ->json_is('/photos/1/location', { private => 1 }, 'location privacy, but no coordinates')
    ->json_hasnt('/photos/2', 'only the batch')
    ->json_is('/albums', [ { slug => 'trip', title => 'Trip' } ], 'every album');

  like($t->tx->res->json->{photos}[0]{version}, qr/\A[0-9a-f]{40}\z/, 'a version');

  my $meta = $editor->library->meta_path('aaaa0001');
  $meta->spew_utf8($meta->slurp_utf8 =~ s/^title = "first"/title = "edited"/mr);
  $t->get_ok('/api/batch')->json_is('/photos/1/title', 'edited', 'a reload reads the file again');
};

subtest 'renditions' => sub {
  my ($editor, $t) = signed_in(
    photos => [ { id => 'aaaa0001' }, { id => 'cccc0003' } ],
    ids    => [ 'aaaa0001' ],
  );

  $t->get_ok('/r/aaaa0001/h480.webp')->status_is(200)->content_is('placeholder');
  $t->get_ok('/r/cccc0003/h480.webp')->status_is(404, 'only photos in the batch');
  $t->get_ok('/r/aaaa0001/og.jpg')->status_is(404, 'only renditions the editor uses');
};

done_testing;
