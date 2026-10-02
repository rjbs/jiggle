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
# Paths in %fail always get a 500, and the next $limited purge requests get
# a 429.  -- claude, 2026-10-01
my (%stored, @purged, %fail, $limited);

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

# Bunny's rule: a URL ending in a slash is a prefix unless exactPath is true.
post '/purge' => sub ($c) {
  return $c->render(text => 'no', status => 401) unless ($c->req->headers->header('AccessKey') // '') eq 'key';
  if ($limited) {
    $limited--;
    $c->res->headers->header('Retry-After' => 0);
    return $c->render(text => 'slow down', status => 429);
  }

  my $url = $c->param('url');
  my $prefix = $url =~ m{/\z} && ($c->param('exactPath') // '') ne 'true';
  push @purged, ($prefix ? 'prefix ' : 'exact ') . $url;
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
    prefix_over  => 2,
    retry_delay  => 0,
    ua           => $ua,
    %arg,
  });
}

sub exact  (@paths) { map {; "exact https://photos.example.com$_" }  @paths }
sub prefix (@paths) { map {; "prefix https://photos.example.com$_" } @paths }

# An index.html, and the two directory URLs that serve it.
sub page ($dir) { exact("$dir/index.html", "$dir/", $dir) }

# Build the site from $files, sync it, and check what was uploaded, deleted,
# and purged, and what the zone holds afterward.  With fail, those paths
# fail every attempt, and the sync should die naming them.  With limited,
# that many purge requests are refused as too many first.
sub sync_ok ($desc, $files, $want, %arg) {
  subtest $desc => sub {
    build_site($files);
    @purged  = ();
    %fail    = map {; $_ => 1 } ($arg{fail} // [])->@*;
    $limited = $arg{limited} // 0;

    my $result = eval { syncer()->sync({ dry_run => $arg{dry_run}, purge_all => $arg{purge_all} }) };
    my $error  = $@;

    if (my @failing = ($arg{fail} // [])->@*) {
      like($error, qr/\Q$_\E/, "died, naming $_") for @failing;
    } else {
      is($error, '', 'lived');
      is_deeply($result->{uploaded}, $want->{uploaded}, 'uploaded');
      is_deeply($result->{deleted},  $want->{deleted},  'deleted');
    }

    is_deeply([ sort @purged ], [ sort { $a cmp $b } ($want->{purged} // [])->@* ], 'purged');
    is($limited, 0, 'every refused purge was tried again') if $arg{limited};
    is_deeply(\%stored, $want->{stored}, 'what the zone holds');
  };
}

my %v1 = (
  'index.html'        => 'home',
  'p/aa/index.html'   => 'photo a',
  'img/aa/500.webp'   => 'a pixels',
  'p/bb/index.html'   => 'photo b',
  'img/bb/500.webp'   => 'b pixels',
);

sync_ok('a dry run changes nothing', \%v1,
  { uploaded => [ sort keys %v1 ], deleted => [], stored => {} },
  dry_run => 1,
);

sync_ok('the first sync uploads everything, and purges it', \%v1,
  {
    uploaded => [ sort keys %v1 ],
    deleted  => [],
    purged   => [
      exact('/index.html', '/'),
      exact('/img/aa/500.webp', '/img/bb/500.webp'),
      page('/p/aa'), page('/p/bb'),
    ],
    stored => \%v1,
  },
);

sync_ok('with nothing changed, nothing is done', \%v1,
  { uploaded => [], deleted => [], stored => \%v1 },
);

my %v2 = %v1;
delete @v2{'p/bb/index.html', 'img/bb/500.webp'};
$v2{'index.html'} = 'home, without b';

sync_ok('a changed page is uploaded, and a removed photo deleted, then purged',
  \%v2,
  {
    uploaded => [ 'index.html' ],
    deleted  => [ 'img/bb/500.webp', 'p/bb/index.html' ],
    purged   => [ exact('/index.html', '/', '/img/bb/500.webp'), page('/p/bb') ],
    stored   => \%v2,
  },
);

my %v3 = %v2;
$v3{'p/aa/index.html'} = 'photo a, retitled';
$v3{'p/cc/index.html'} = 'photo c';
delete $v3{'img/aa/500.webp'};

sync_ok('if an upload fails, nothing is deleted, but what was uploaded is purged',
  \%v3,
  {
    purged => [ page('/p/aa') ],
    stored => { %v2, 'p/aa/index.html' => 'photo a, retitled' },
  },
  fail => [ 'p/cc/index.html' ],
);

sync_ok('the next sync does only what is left', \%v3,
  {
    uploaded => [ 'p/cc/index.html' ],
    deleted  => [ 'img/aa/500.webp' ],
    purged   => [ page('/p/cc'), exact('/img/aa/500.webp') ],
    stored   => \%v3,
  },
);

my %v4 = (%v3, '404.html' => 'not found');

sync_ok('the 404 page is uploaded where Bunny looks for it, too', \%v4,
  {
    uploaded => [ '404.html', 'bunnycdn_errors/404.html' ],
    deleted  => [],
    purged   => [ exact('/404.html', '/bunnycdn_errors/404.html') ],
    stored   => { %v4, 'bunnycdn_errors/404.html' => 'not found' },
  },
);

my %v5 = (
  %v4,
  'index.html'        => 'home, new style',
  'p/aa/index.html'   => 'photo a, new style',
  'p/cc/index.html'   => 'photo c, new style',
  'p/dd/index.html'   => 'photo d',
  'img/dd/500.webp'   => 'd pixels',
  'albums/index.html' => 'albums',
  'albums/x/index.html' => 'album x',
  'albums/y/index.html' => 'album y',
);

sync_ok('a directory with many changes is purged by prefix, even when Bunny says to slow down',
  \%v5,
  {
    uploaded => [
      'albums/index.html', 'albums/x/index.html', 'albums/y/index.html',
      'img/dd/500.webp', 'index.html',
      'p/aa/index.html', 'p/cc/index.html', 'p/dd/index.html',
    ],
    deleted  => [],
    purged   => [
      prefix('/albums/', '/p/'),
      exact('/albums', '/img/dd/500.webp', '/index.html', '/'),
    ],
    stored   => { %v5, 'bunnycdn_errors/404.html' => 'not found' },
  },
  limited => 3,
);

sync_ok('purging everything, on request', \%v5,
  {
    uploaded => [],
    deleted  => [],
    purged   => [ 'ALL' ],
    stored   => { %v5, 'bunnycdn_errors/404.html' => 'not found' },
  },
  purge_all => 1,
);

subtest 'a file put in the zone some other way is left alone' => sub {
  $stored{'from-the-dashboard.txt'} = 'hello';
  build_site(\%v5);
  my $result = syncer()->sync;
  is_deeply($result->{deleted}, [], 'not deleted');
  ok(exists $stored{'from-the-dashboard.txt'}, '...and still there');
};

done_testing;
