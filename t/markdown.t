use v5.36;

use Test::More;

use lib 'lib';

use HTML::Parser ();
use Jiggle::Markdown qw( html_to_markdown markdown_to_html markdown_to_text );

sub squish ($s) { $s =~ s/\s+/ /gr =~ s/\A | \z//gr }

# The text a browser would show for some HTML.
sub html_text ($html) {
  my $text = '';
  my $p = HTML::Parser->new(
    api_version => 3,
    text_h  => [ sub ($t) { $text .= $t }, 'dtext' ],
    start_h => [ sub ($tag) { $text .= ' ' if $tag eq 'br' || $tag eq 'p' }, 'tagname' ],
  );
  $p->parse($html);
  $p->eof;
  return $text;
}

# Convert, check the Markdown, and check that rendering the Markdown shows
# the same text the HTML did: nothing was lost, and nothing literal turned
# into syntax.
sub converts_ok ($desc, $html, $want) {
  my $md = html_to_markdown($html);
  is($md, $want, "markdown: $desc");
  is(
    squish(markdown_to_text($md)),
    squish(html_text($html)),
    "same text: $desc",
  );
}

sub renders_link_ok ($desc, $html, $href) {
  like(
    markdown_to_html(html_to_markdown($html)),
    qr{<a href="\Q$href\E">},
    "link survives: $desc",
  );
}

converts_ok('plain text', 'We also know how to use them.', 'We also know how to use them.');
converts_ok('entities', 'saying, &quot;That&#39;s what I want!&quot;', q{saying, "That's what I want!"});
converts_ok('newlines kept', "Bad: the shower\n\nGood: heated floor", "Bad: the shower\n\nGood: heated floor");
converts_ok('br', 'one<br>two<br />three', "one\ntwo\nthree");

converts_ok('link',
  'see <a href="http://example.com/a">the photos</a>',
  'see [the photos](http://example.com/a)');

converts_ok('link whose text is its URL',
  'more: <a href="http://www.flickr.com/photos/x/1/">http://www.flickr.com/photos/x/1/</a>',
  'more: <http://www.flickr.com/photos/x/1/>');

converts_ok('link whose text is its URL without the scheme',
  'more: <a href="http://www.flickr.com/photos/x/1/">www.flickr.com/photos/x/1/</a>',
  'more: [www.flickr.com/photos/x/1/](http://www.flickr.com/photos/x/1/)');

converts_ok('bare URL, sentence punctuation left outside',
  'See http://example.com/page.',
  'See <http://example.com/page>.');

converts_ok('emphasis', 'a <b>big</b> and <i>small</i> deal', 'a **big** and *small* deal');
converts_ok('unknown tags dropped', 'a <span class="x">b</span> <u>c</u>', 'a b c');
converts_ok('blockquote', 'he said: <blockquote>no</blockquote>', "he said:\n\n> no");

converts_ok('literal asterisks', 'I *love* this', 'I \*love\* this');
converts_ok('underscores in words are safe', 'my_file_name', 'my_file_name');
converts_ok('underscores at edges are not', 'an _aside_ here', 'an \_aside\_ here');
converts_ok('brackets', 'a [note] here', 'a \[note\] here');
converts_ok('heading-like line', '# of photos: 3', '\# of photos: 3');
converts_ok('list-like lines', "- one\n2. two", "\\- one\n2\\. two");
converts_ok('entity-like text', 'write &amp;copy; for it', 'write \&copy; for it');
converts_ok('angle brackets', 'a &lt;tag&gt; here', 'a \<tag> here');
converts_ok('backslash', 'C:\\photos', 'C:\\\\photos');

renders_link_ok('ordinary link', '<a href="http://example.com/a">x</a>', 'http://example.com/a');
renders_link_ok('autolink', '<a href="http://example.com/">http://example.com/</a>', 'http://example.com/');
renders_link_ok('parens in URL', '<a href="http://example.com/a_(b)">x</a>', 'http://example.com/a_%28b%29');

is(html_to_markdown(''), '', 'empty stays empty');

done_testing;
