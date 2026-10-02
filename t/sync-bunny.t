use v5.36;

use Test::More;

use lib 'lib';

use Digest::SHA ();
use JSON::MaybeXS ();
use Jiggle::Sync::Bunny;
use Mojolicious::Lite -signatures;
use Path::Tiny ();

# A fake Bunny: the storage API, which checks the password and checksum of
# each upload, and the purge API, which records what it was asked to purge.
# Paths in %fail always get a 500.  -- claude, 2026-10-01
my (%stored, @purged, %fail);

app->log->level('fatal');

put '/zone/*rel' => sub ($c) {
  my $path = $c->stash('rel');
  return $c->render(text => 'no', status => 401) unless ($c->req->headers->header('AccessKey') // '') eq 'pw';
  return $c->render(text => 'broken', status => 500) if $fail{$path};

  my $body = $c->req->body;
  return $c->render(text => 'bad checksum', status => 400)
    unless ($c->req->headers->header('Checksum') // '') eq uc Digest::SHA::sha256_hex($body);

  $stored{$path} = $body;
  $c->render(json => { HttpCode => 201 }, status => 201);
};

del '/zone/*rel' => sub ($c) {
  my $path = $c->stash('rel');
  return $c->render(text => 'broken', status => 500) if $fail{$path};
  return $c->render(text => 'nope', status => 404) unless exists $stored{$path};
  delete $stored{$path};
  $c->render(json => { HttpCode => 200 });
};

post '/purge' => sub ($c) {
  return $c->render(text => 'no', status => 401) unless ($c->req->headers->header('AccessKey') // '') eq 'key';
  push @purged, $c->param('url');
  $c->rendered(204);
};

post '/pullzone/77/purgeCache' => sub ($c) {
  push @purged, 'ALL';
  $c->rendered(204);
};

my $tmp  = Path::Tiny->tempdir;
my $site = $tmp->child('site');
my $JSON = JSON::MaybeXS->new->canonical->utf8;

my $ua = Mojo::UserAgent->new;
$ua->server->app(app);
my $base = $ua->server->nb_url->to_string =~ s{/\z}{}r;

# Make site/ hold exactly these files, with a build manifest to match.
sub build_site ($files) {
  $site->remove_tree;
  my %manifest;
  for my $rel (keys %$files) {
    $site->child($rel)->touchpath->spew_raw($files->{$rel});
    $manifest{$rel} = { sha1 => Digest::SHA::sha1_hex($files->{$rel}) };
  }
  $tmp->child('state', 'site-manifest.json')->touchpath
    ->spew_raw($JSON->encode({ version => 1, files => \%manifest }));
}

sub syncer (%arg) {
  Jiggle::Sync::Bunny->new({
    site_dir     => $site,
    state_dir    => $tmp->child('state'),
    zone         => 'zone',
    password     => 'pw',
    storage_url  => $base,
    api_key      => 'key',
    pull_zone_id => 77,
    api_url      => $base,
    purge_hosts  => [ 'photos.example.com' ],
    purge_all_over => 4,
    retry_delay  => 0,
    ua           => $ua,
    %arg,
  });
}

# Build the site from $files, sync it, and check what was uploaded, deleted,
# and purged, and what the zone holds afterward.  With fail, those paths
# fail every attempt, and the sync should die naming them.
sub sync_ok ($desc, $files, $want, %arg) {
  subtest $desc => sub {
    build_site($files);
    @purged = ();
    %fail = map {; $_ => 1 } ($arg{fail} // [])->@*;

    my $result = eval { syncer()->sync({ dry_run => $arg{dry_run} }) };
    my $error  = $@;

    if (my @failing = ($arg{fail} // [])->@*) {
      like($error, qr/\Q$_\E/, "died, naming $_") for @failing;
    } else {
      is($error, '', 'lived');
      is_deeply($result->{uploaded}, $want->{uploaded}, 'uploaded');
      is_deeply($result->{deleted},  $want->{deleted},  'deleted');
    }

    is_deeply([ sort @purged ], [ sort { $a cmp $b } ($want->{purged} // [])->@* ], 'purged');
    is_deeply(\%stored, $want->{stored}, 'what the zone holds');
  };
}

my %v1 = (
  'index.html'        => 'home',
  'p/aa/index.html'   => 'photo a',
  'p/aa/500.webp'     => 'a pixels',
  'p/bb/index.html'   => 'photo b',
  'p/bb/500.webp'     => 'b pixels',
);

sync_ok('a dry run changes nothing', \%v1,
  { uploaded => [ sort keys %v1 ], deleted => [], stored => {} },
  dry_run => 1,
);

sync_ok('the first sync uploads everything, and purges the whole zone', \%v1,
  { uploaded => [ sort keys %v1 ], deleted => [], purged => [ 'ALL' ], stored => \%v1 },
);

sync_ok('with nothing changed, nothing is done', \%v1,
  { uploaded => [], deleted => [], stored => \%v1 },
);

my %v2 = %v1;
delete @v2{'p/bb/index.html', 'p/bb/500.webp'};
$v2{'index.html'} = 'home, without b';

sync_ok('a changed page is uploaded, and a removed photo deleted, then purged',
  \%v2,
  {
    uploaded => [ 'index.html' ],
    deleted  => [ 'p/bb/500.webp', 'p/bb/index.html' ],
    purged   => [
      'https://photos.example.com/index.html',
      'https://photos.example.com/',
      'https://photos.example.com/p/bb/500.webp',
      'https://photos.example.com/p/bb/index.html',
      'https://photos.example.com/p/bb/',
      'https://photos.example.com/p/bb',
    ],
    stored => \%v2,
  },
);

my %v3 = %v2;
$v3{'p/aa/index.html'} = 'photo a, retitled';
$v3{'p/cc/index.html'} = 'photo c';
delete $v3{'p/aa/500.webp'};

sync_ok('if an upload fails, nothing is deleted, but what was uploaded is purged',
  \%v3,
  {
    purged => [
      'https://photos.example.com/p/aa/index.html',
      'https://photos.example.com/p/aa/',
      'https://photos.example.com/p/aa',
    ],
    stored => { %v2, 'p/aa/index.html' => 'photo a, retitled' },
  },
  fail => [ 'p/cc/index.html' ],
);

sync_ok('the next sync does only what is left', \%v3,
  {
    uploaded => [ 'p/cc/index.html' ],
    deleted  => [ 'p/aa/500.webp' ],
    purged   => [
      'https://photos.example.com/p/cc/index.html',
      'https://photos.example.com/p/cc/',
      'https://photos.example.com/p/cc',
      'https://photos.example.com/p/aa/500.webp',
    ],
    stored => \%v3,
  },
);

my %v4 = (%v3, '404.html' => 'not found');

sync_ok('the 404 page is uploaded where Bunny looks for it, too', \%v4,
  {
    uploaded => [ '404.html', 'bunnycdn_errors/404.html' ],
    deleted  => [],
    purged   => [
      'https://photos.example.com/404.html',
      'https://photos.example.com/bunnycdn_errors/404.html',
    ],
    stored => { %v4, 'bunnycdn_errors/404.html' => 'not found' },
  },
);

subtest 'a file put in the zone some other way is left alone' => sub {
  $stored{'from-the-dashboard.txt'} = 'hello';
  build_site(\%v4);
  my $result = syncer()->sync;
  is_deeply($result->{deleted}, [], 'not deleted');
  ok(exists $stored{'from-the-dashboard.txt'}, '...and still there');
};

done_testing;
