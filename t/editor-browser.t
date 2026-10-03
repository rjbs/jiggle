use v5.36;

use Test::More;

use lib 'lib', 't/lib';

use Jiggle::Album;
use Jiggle::Editor;
use Jiggle::TestLibrary;
use JSON::MaybeXS ();
use Mojo::IOLoop::Server;
use Mojo::Server::Daemon;

# This drives the editor in a real browser, with Playwright, which isn't a
# dependency: set JIGGLE_PLAYWRIGHT to a directory where it's installed (with
# its browsers where it can find them, as by PLAYWRIGHT_BROWSERS_PATH).
# -- claude, 2026-10-03
plan skip_all => 'set JIGGLE_PLAYWRIGHT to run browser tests' unless $ENV{JIGGLE_PLAYWRIGHT};
plan skip_all => 'needs node' unless system('node --version >/dev/null 2>&1') == 0;

$ENV{GIT_AUTHOR_NAME}  = $ENV{GIT_COMMITTER_NAME}  = 'Test';
$ENV{GIT_AUTHOR_EMAIL} = $ENV{GIT_COMMITTER_EMAIL} = 'test@example.com';

my ($library) = library_with(
  photos => [
    { id => 'aaaa0001', title => 'one',   taken => '2026-07-01T10:00:00' },
    { id => 'bbbb0002', title => 'two',   taken => '2026-07-02T10:00:00' },
    { id => 'cccc0003', title => 'three', taken => '2026-07-03T10:00:00' },
    { id => 'pend0001', pending => 1, taken => '2026-09-01T10:00:00' },
    { id => 'pend0002', pending => 1, taken => '2026-09-02T10:00:00' },
    { id => 'pend0003', pending => 1, taken => '2026-09-03T10:00:00' },
    { id => 'priv0001', visibility => 'private' },
  ],
  albums => [
    { slug => 'trip',   title => 'Trip',   cover => 'aaaa0001', photos => [ qw( aaaa0001 bbbb0002 cccc0003 ) ] },
    { slug => 'hidden', title => 'Hidden', photos => [ 'priv0001' ] },
  ],
);

my $meta = $library->meta_dir;
system("git -C '$meta' init --quiet && git -C '$meta' add . && git -C '$meta' commit --quiet -m first") == 0
  or die "can't make meta/ a repository";

# The editor runs in a child process, so the browser can reach it while
# this one waits for the browser.
my $editor = Jiggle::Editor->new({ library => $library });
my $token  = $editor->token;    # made now, or the child would make its own
my $port   = Mojo::IOLoop::Server->generate_port;
my $pid = fork // die "can't fork: $!";
unless ($pid) {
  my $daemon = Mojo::Server::Daemon->new(app => $editor->app, listen => [ "http://127.0.0.1:$port" ], silent => 1);
  $daemon->run;
  exit;
}
END { kill TERM => $pid if $pid }
sleep 1;

my $out = `node t/browser/editor.mjs 'http://127.0.0.1:$port/?token=$token'`;
my $seen = eval { JSON::MaybeXS::decode_json($out) } or BAIL_OUT("no report from the browser: $out");

sub saw ($key, $want) {
  is_deeply($seen->{$key}, $want, $key) or diag explain $seen->{$key};
}

sub album_is ($slug, $field, $want) {
  my $album = Jiggle::Album->from_toml_file($library->albums_dir->child("$slug.toml"));
  is_deeply($album->$field, $want, "on disk: ${slug}'s $field");
}

saw(albums       => [ 'Hidden', 'Trip' ]);
saw(album_counts => [ '1 private', '3 published' ]);

saw(album_query        => 'album:trip');
saw(album_order        => [ qw( aaaa0001 bbbb0002 cccc0003 ) ]);
saw(album_order_edited => [ qw( cccc0003 aaaa0001 bbbb0002 ) ]);
saw(album_write_button => 'Write changes (album)');
album_is(trip => photos => [ qw( cccc0003 aaaa0001 bbbb0002 ) ]);
album_is(trip => cover  => 'cccc0003');

saw(pending_query               => 'pending limit:2');
saw(pending_first               => [ qw( pend0001 pend0002 ) ]);
saw(refresh_disabled_when_dirty => JSON::MaybeXS::true);
saw(pending_next                => [ qw( pend0002 pend0003 ) ]);
unlike($library->meta_path('pend0001')->slurp_utf8, qr/^pending/m, 'on disk: pend0001 released');

saw(back_query       => 'album:trip');
saw(bad_query_status => 'unknown query term: pendng');
saw(after_bad_query  => 'album:trip');
saw(dialog           => "You have changes that aren't written.  Discard them?");
like($library->meta_path('aaaa0001')->slurp_utf8, qr/^title = "one"$/m, 'on disk: the discarded edit');

saw(errors => []);

done_testing;
