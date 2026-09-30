use v5.36;

use Test::More;
use Test::Mojo;

use lib 'lib', 't/lib';

use Jiggle::Album;
use Jiggle::Editor;
use Jiggle::TestLibrary;

# Derive is replaced by something that only records what it was asked to
# remake, so these tests don't need libvips.
package Recording::Derive {
  sub new ($class) { bless { remade => [] }, $class }
  sub derive_photos ($self, @photos) { push $self->{remade}->@*, map {; $_->id } @photos; 0 }
}

sub editor_for (%arg) {
  my $ids = delete $arg{ids};
  my ($library) = library_with(%arg);
  my $editor = Jiggle::Editor->new({ library => $library, ids => $ids, derive => Recording::Derive->new });
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
    ->json_is('/albums', [ { slug => 'trip', title => 'Trip' } ], 'every album')
    ->json_is('/tags', [ 'x' ], 'every tag');

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

#---------------------------------------------------------------------------
# Writing

$ENV{GIT_AUTHOR_NAME}    = $ENV{GIT_COMMITTER_NAME}  = 'Test';
$ENV{GIT_AUTHOR_EMAIL}   = $ENV{GIT_COMMITTER_EMAIL} = 'test@example.com';

sub git ($library, @args) {
  my $meta = $library->meta_dir;
  my $out = `git -C '$meta' @args 2>&1`;
  die "git @args: $out" if $?;
  return $out;
}

# An editor on a library whose meta/ is a git repository with everything
# committed, and a client signed in to it.
sub editing (%arg) {
  my ($editor, $t) = signed_in(%arg);
  git($editor->library, 'init --quiet');
  git($editor->library, 'add .');
  git($editor->library, "commit --quiet -m 'first'");
  return ($editor, $t);
}

sub version_of ($t, $id) {
  $t->get_ok('/api/batch');
  my ($photo) = grep {; $_->{id} eq $id } $t->tx->res->json->{photos}->@*;
  return $photo->{version};
}

# Writes changes to one photo, as loaded, returning the response's JSON.
sub write_one ($t, $id, $changes, %extra) {
  $t->post_ok('/api/write', json => {
    photos => [ { id => $id, version => version_of($t, $id), changes => $changes } ],
    %extra,
  });
  return $t->tx->res->json;
}

sub meta_of ($editor, $id) { $editor->library->meta_path($id)->slurp_utf8 }

sub writes_ok ($desc, $spec, $changes, @want) {
  subtest "writes: $desc" => sub {
    my ($editor, $t) = editing(photos => [ { id => 'aaaa0001', %$spec } ], ids => [ 'aaaa0001' ]);
    my $result = write_one($t, 'aaaa0001', $changes);
    $t->status_is(200);
    like(meta_of($editor, 'aaaa0001'), $_) for @want;
    like($result->{commit} // '', qr/\A[0-9a-f]{7,}\z/, 'committed');
    like(git($editor->library, 'log -1 --format=%B'), qr/\Aedit 1 photo\(s\): \w+\n+\z/, '...with no note');
    is(git($editor->library, 'status --porcelain'), '', 'nothing left uncommitted');
  };
}

sub refuses_ok ($desc, $spec, $changes, $want_error) {
  subtest "refuses: $desc" => sub {
    my ($editor, $t) = editing(photos => [ { id => 'aaaa0001', %$spec } ], ids => [ 'aaaa0001' ]);
    my $before = meta_of($editor, 'aaaa0001');
    my $result = write_one($t, 'aaaa0001', $changes);
    $t->status_is(400);
    like($result->{error}, $want_error, 'the error');
    is(meta_of($editor, 'aaaa0001'), $before, 'nothing written');
  };
}

writes_ok('title', { title => 'old' }, { title => 'new' }, qr/^title = "new"$/m);
writes_ok('description', {}, { description => "two\nlines" }, qr/^description = """\ntwo\nlines"""$/m);
writes_ok('tags, trimmed and deduplicated', { tags => [ 'x' ] },
  { tags => [ ' x ', 'y', 'x', '' ] }, qr/^tags = \["x", "y"\]$/m);
writes_ok('tags, lowercased', {}, { tags => [ 'FastMail', 'fastmail' ] }, qr/^tags = \["fastmail"\]$/m);
writes_ok('visibility', {}, { visibility => 'private' }, qr/^visibility = "private"$/m);
writes_ok('pending cleared', { pending => 1 }, { pending => 0 }, qr/\A(?!.*^pending)/ms);
writes_ok('taken', {}, { taken => '2026-09-30T10:15:00-04:00' }, qr/^taken = 2026-09-30T10:15:00-04:00$/m);
writes_ok('taken removed', { taken => '2026-09-30T10:15:00' }, { taken => '' }, qr/\A(?!.*^taken)/ms);
writes_ok('rotate', {}, { rotate => 270 }, qr/^rotate = 270$/m);
writes_ok('location made private', { location => { lat => 40, lon => -75 } },
  { location_private => 1 }, qr/^private = true$/m);

refuses_ok('an unknown field', {}, { flickr_id => '1' }, qr/can't change flickr_id/);
refuses_ok('a bad rotation', {}, { rotate => 45 }, qr/rotate must be/);
refuses_ok('a bad date', {}, { taken => 'yesterday' }, qr/taken must be a datetime/);
refuses_ok('a bad visibility', {}, { visibility => 'friends' }, qr/unknown visibility/);
refuses_ok('privacy for no location', {}, { location_private => 1 }, qr/no location/);

subtest 'the commit' => sub {
  my ($editor, $t) = editing(
    photos => [ { id => 'aaaa0001' }, { id => 'bbbb0002' }, { id => 'cccc0003' } ],
    ids    => [ 'aaaa0001', 'bbbb0002' ],
  );

  # Unrelated work in progress in meta/ stays out of the commit.
  my $other = $editor->library->meta_path('cccc0003');
  $other->spew_utf8($other->slurp_utf8 =~ s/^title = ""/title = "by hand"/mr);

  $t->post_ok('/api/write', json => {
    note   => 'from the trip',
    photos => [
      { id => 'aaaa0001', version => version_of($t, 'aaaa0001'), changes => { title => 'one' } },
      { id => 'bbbb0002', version => version_of($t, 'bbbb0002'), changes => { tags => [ 'x' ] } },
    ],
  })->status_is(200)
    ->json_is('/photos/0/title', 'one', 'the written photos come back')
    ->json_is('/photos/1/tags', [ 'x' ]);

  is(git($editor->library, 'log -1 --format=%B'), "edit 2 photo(s): tags, title\n\nfrom the trip\n\n",
    'the message names the fields, and the note');
  is(git($editor->library, 'show --name-only --format= HEAD'),
    "aa/aaaa0001.toml\nbb/bbbb0002.toml\n", 'only the written files');
  is(git($editor->library, 'status --porcelain'), " M cc/cccc0003.toml\n", 'the other change is left alone');

  is($t->tx->res->json->{photos}[0]{version}, version_of($t, 'aaaa0001'), 'the new version is current');
};

subtest 'a turned photo has its renditions remade' => sub {
  my ($editor, $t) = editing(photos => [ { id => 'aaaa0001' }, { id => 'bbbb0002' } ], ids => [ 'aaaa0001', 'bbbb0002' ]);
  $t->post_ok('/api/write', json => {
    photos => [
      { id => 'aaaa0001', version => version_of($t, 'aaaa0001'), changes => { rotate => 90 } },
      { id => 'bbbb0002', version => version_of($t, 'bbbb0002'), changes => { title => 'x' } },
    ],
  })->status_is(200);
  is_deeply($editor->derive->{remade}, [ 'aaaa0001' ], 'only the turned one');
};

subtest 'changed on disk since loading' => sub {
  my ($editor, $t) = editing(photos => [ { id => 'aaaa0001' }, { id => 'bbbb0002' } ], ids => [ 'aaaa0001', 'bbbb0002' ]);
  my ($va, $vb) = (version_of($t, 'aaaa0001'), version_of($t, 'bbbb0002'));

  my $meta = $editor->library->meta_path('bbbb0002');
  $meta->spew_utf8($meta->slurp_utf8 =~ s/^title = ""/title = "by hand"/mr);

  $t->post_ok('/api/write', json => {
    photos => [
      { id => 'aaaa0001', version => $va, changes => { title => 'mine' } },
      { id => 'bbbb0002', version => $vb, changes => { title => 'mine' } },
    ],
  })->status_is(409)->json_is('/conflicts', [ 'bbbb0002' ]);

  like(meta_of($editor, 'bbbb0002'), qr/^title = "by hand"$/m, 'the hand edit is kept');
  like(meta_of($editor, 'aaaa0001'), qr/^title = ""$/m, 'and nothing else is written either');
};

subtest 'unchanged, and outside the batch' => sub {
  my ($editor, $t) = editing(photos => [ { id => 'aaaa0001', title => 'same' }, { id => 'cccc0003' } ], ids => [ 'aaaa0001' ]);

  my $head = git($editor->library, 'rev-parse HEAD');
  is(write_one($t, 'aaaa0001', { title => 'same' })->{commit}, undef, 'no change, no commit');
  is(git($editor->library, 'rev-parse HEAD'), $head, '...and HEAD is where it was');

  $t->post_ok('/api/write', json => { photos => [ { id => 'cccc0003', version => 'x', changes => { title => 'no' } } ] })
    ->status_is(400)->json_like('/error', qr/not in this batch/);
};

subtest 'writing needs the token' => sub {
  my ($editor, $t) = editor_for(photos => [ { id => 'aaaa0001' } ], ids => [ 'aaaa0001' ]);
  $t->post_ok('/api/write', json => { photos => [] })->status_is(403);
};

#---------------------------------------------------------------------------
# Albums

sub album_of ($editor, $slug) {
  my $file = $editor->library->albums_dir->child("$slug.toml");
  return -e $file ? Jiggle::Album->from_toml_file($file) : undef;
}

# Writes album changes for photos (each { id => { add => [...], remove => [...] } }),
# with any new albums, and returns the response's JSON.
sub write_albums ($t, $changes, @new_albums) {
  $t->post_ok('/api/write', json => {
    new_albums => \@new_albums,
    photos => [
      map {; { id => $_, version => version_of($t, $_), changes => { albums => $changes->{$_} } } }
      sort keys %$changes
    ],
  });
  return $t->tx->res->json;
}

sub album_edit_ok ($desc, $albums, $changes, $new_albums, $want) {
  subtest "albums: $desc" => sub {
    my ($editor, $t) = editing(
      photos => [ map {; { id => $_ } } qw( aaaa0001 bbbb0002 cccc0003 ) ],
      albums => $albums,
      ids    => [ qw( aaaa0001 bbbb0002 cccc0003 ) ],
    );
    my $result = write_albums($t, $changes, @$new_albums);
    $t->status_is(200) or diag explain $result;

    for my $slug (sort keys %$want) {
      my $album = album_of($editor, $slug);
      ok($album, "$slug exists") or next;
      is_deeply($album->photos, $want->{$slug}{photos}, "$slug: photos");
      is($album->cover, $want->{$slug}{cover}, "$slug: cover") if exists $want->{$slug}{cover};
    }
    is(git($editor->library, 'status --porcelain'), '', 'everything committed');
  };
}

my $TRIP = { slug => 'trip', title => 'Trip', photos => [ 'aaaa0001' ] };

album_edit_ok('added at the end, in batch order', [ $TRIP ],
  { cccc0003 => { add => [ 'trip' ] }, bbbb0002 => { add => [ 'trip' ] } }, [],
  { trip => { photos => [ qw( aaaa0001 bbbb0002 cccc0003 ) ] } });

album_edit_ok('removed, and the cover moves on',
  [ { %$TRIP, photos => [ qw( aaaa0001 bbbb0002 ) ] } ],
  { aaaa0001 => { remove => [ 'trip' ] } }, [],
  { trip => { photos => [ 'bbbb0002' ], cover => 'bbbb0002' } });

album_edit_ok('a new album', [ $TRIP ],
  { aaaa0001 => { add => [ 'new-1' ] }, cccc0003 => { add => [ 'new-1' ] } },
  [ { key => 'new-1', title => 'Berlin, 2026-07' } ],
  { 'berlin-2026-07' => { photos => [ qw( aaaa0001 cccc0003 ) ], cover => 'aaaa0001' },
    trip             => { photos => [ 'aaaa0001' ] } });

album_edit_ok('a new album whose slug is taken', [ { %$TRIP, slug => 'trip' } ],
  { bbbb0002 => { add => [ 'k' ] } }, [ { key => 'k', title => 'Trip' } ],
  { 'trip-2' => { photos => [ 'bbbb0002' ] } });

subtest 'albums: the details' => sub {
  my ($editor, $t) = editing(
    photos => [ map {; { id => $_ } } qw( aaaa0001 bbbb0002 ) ],
    albums => [ $TRIP ],
    ids    => [ qw( aaaa0001 bbbb0002 ) ],
  );

  # A hand edit to the album file after loading is merged, not lost.
  my $file = $editor->library->albums_dir->child('trip.toml');
  $file->spew_utf8($file->slurp_utf8 =~ s/^title = "Trip"/title = "Trip, retitled"/mr);

  my $result = write_albums($t, { bbbb0002 => { add => [ 'trip' ] } },
    { key => 'unused', title => 'Never Used' });
  $t->status_is(200);

  is(album_of($editor, 'trip')->title, 'Trip, retitled', 'the hand edit is kept');
  is_deeply(album_of($editor, 'trip')->photos, [ qw( aaaa0001 bbbb0002 ) ], '...along with the addition');
  is(album_of($editor, 'never-used'), undef, 'a new album given no photos is not made');
  like(git($editor->library, 'log -1 --format=%s'), qr/\Aedit 1 photo\(s\): albums\n*\z/, 'the message');
  is_deeply([ map {; $_->{albums} } $result->{photos}->@* ], [ [ 'trip' ] ], 'the photo comes back in its album');
  is($result->{albums}[0]{title}, 'Trip, retitled', 'the albums come back');

  write_albums($t, { aaaa0001 => { add => [ 'nope' ] } });
  $t->status_is(400)->json_like('/error', qr/no album nope/);
};

done_testing;
