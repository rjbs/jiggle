package Jiggle::App::Command::edit;
use v5.36;

use Jiggle::App -command;

use Jiggle::Editor;

sub abstract { 'review and edit photos and albums in the browser' }

sub usage_desc { '%c edit %o [QUERY...]' }

sub description {
  return <<~'END';
  This serves an editor on localhost, for a batch of photos chosen by a
  query, typed into the page or given here.  The batch is every photo
  matching all the query's terms:

    pending  private  public  unlisted  all
    album:SLUG  tag:TAG  year:YYYY  id:ID  limit:N

  For example, "jiggle edit pending limit:50" reviews newly ingested photos,
  fifty at a time; the page's Refresh button picks the next fifty, once the
  first are no longer pending.  Without a query, it opens on the list of
  albums.

  Given exactly one term, album:SLUG, it edits the album too: its title,
  description, cover, and order.

  END
}

sub opt_spec {
  return (
    [ 'port|p=i', 'port to listen on', { default => 3001 } ],
    [ 'no-open',  "don't open the editor in a browser" ],
  );
}

sub execute ($self, $opt, $args) {
  Jiggle::Editor->serve_query($self->library, $args, {
    port => $opt->port,
    open => ! $opt->no_open,
  });
}

1;
